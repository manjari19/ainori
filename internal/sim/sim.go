package sim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"ainori/internal/geo"
)

type Config struct {
	Target    string // gateway base URL, e.g. http://localhost:8080
	Drivers   int
	Riders    int
	Rate      float64 // ride requests per second
	Speed     float64 // how many times faster than real time drivers move
	Seed      int64
	PingEvery time.Duration // driver ping interval
	Drain     time.Duration // how long to wait for outcomes after the last request
	Logf      func(format string, args ...any)
}

func (c *Config) defaults() {
	if c.Rate <= 0 {
		c.Rate = 10
	}
	if c.Speed <= 0 {
		c.Speed = 10
	}
	if c.PingEvery <= 0 {
		c.PingEvery = 2 * time.Second
	}
	if c.Drain <= 0 {
		c.Drain = 10 * time.Second
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	c.Target = strings.TrimRight(c.Target, "/")
}

// Minutes from "now" until class starts. Short ones make some riders genuinely late.
var classSlots = []int{35, 65, 125, 185}

type driverSpec struct {
	body    map[string]any
	route   *Route
	startKm float64
}

type riderSpec struct {
	body    map[string]any
	classIn int
}

type outcome struct {
	at     time.Time
	typ    string
	reason string
}

type runner struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	outcomes map[string]outcome   // request id -> first outcome seen
	sent     map[string]time.Time // request id -> when we started the POST

	pingErrs atomic.Int64
	reqErrs  atomic.Int64
}

// Run plays the whole scenario against cfg.Target and returns the measured report.
func Run(ctx context.Context, cfg Config) (*Report, error) {
	cfg.defaults()
	rts, err := LoadRoutes()
	if err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	drivers, riders := makeSpecs(rng, rts, cfg)

	r := &runner{
		cfg: cfg,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 512, IdleConnTimeout: 30 * time.Second},
		},
		outcomes: map[string]outcome{}, sent: map[string]time.Time{},
	}

	wsCtx, stopWS := context.WithCancel(ctx)
	defer stopWS()
	if err := r.listen(wsCtx); err != nil {
		return nil, fmt.Errorf("connect websocket: %w", err)
	}

	cfg.Logf("registering %d drivers and %d riders", len(drivers), len(riders))
	driverIDs, err := registerAll(ctx, r, "/drivers", mapSlice(drivers, func(d driverSpec) map[string]any { return d.body }))
	if err != nil {
		return nil, fmt.Errorf("register drivers: %w", err)
	}
	riderIDs, err := registerAll(ctx, r, "/riders", mapSlice(riders, func(d riderSpec) map[string]any { return d.body }))
	if err != nil {
		return nil, fmt.Errorf("register riders: %w", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var drv sync.WaitGroup
	for i, d := range drivers {
		drv.Add(1)
		go func() {
			defer drv.Done()
			r.driveLoop(runCtx, driverIDs[i], d)
		}()
	}

	cfg.Logf("sending %d requests at %.1f/s", len(riders), cfg.Rate)
	startedAt := time.Now()
	sendStart := time.Now()
	var reqWG sync.WaitGroup
	interval := time.Duration(float64(time.Second) / cfg.Rate)
	tick := time.NewTicker(interval)
	// Open loop: the next request goes out on schedule even if earlier ones are slow.
	// A closed loop would hide overload by slowing down with the server.
sendLoop:
	for i := range riders {
		select {
		case <-ctx.Done():
			break sendLoop
		case <-tick.C:
		}
		reqWG.Add(1)
		go func() {
			defer reqWG.Done()
			r.sendRequest(ctx, riderIDs[i], riders[i].classIn)
		}()
	}
	tick.Stop()
	sendEnd := time.Now()
	reqWG.Wait()

	// Wait for outcomes still in flight.
	deadline := time.Now().Add(cfg.Drain)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if r.resolved() >= r.sentCount() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stopRun()
	drv.Wait()

	return r.report(startedAt, sendEnd.Sub(sendStart)), nil
}

func (r *runner) sentCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *runner) resolved() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for id := range r.sent {
		if _, ok := r.outcomes[id]; ok {
			n++
		}
	}
	return n
}

func (r *runner) report(startedAt time.Time, sendDur time.Duration) *Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep := &Report{
		StartedAt: startedAt, Target: r.cfg.Target,
		Params:  Params{Drivers: r.cfg.Drivers, Riders: r.cfg.Riders, Rate: r.cfg.Rate, Speed: r.cfg.Speed, Seed: r.cfg.Seed},
		Reasons: map[string]int{}, RequestError: int(r.reqErrs.Load()), PingErrors: int(r.pingErrs.Load()),
		Requests: len(r.sent), SendSeconds: round1(sendDur.Seconds()),
	}
	var matchedMs, allMs []float64
	for id, sentAt := range r.sent {
		o, ok := r.outcomes[id]
		if !ok {
			rep.NoResponse++
			continue
		}
		ms := float64(o.at.Sub(sentAt).Microseconds()) / 1000
		allMs = append(allMs, ms)
		if o.typ == "matched" {
			rep.Matched++
			matchedMs = append(matchedMs, ms)
		} else {
			rep.Unmatched++
			rep.Reasons[o.reason]++
		}
	}
	if rep.Requests > 0 {
		rep.MatchRate = float64(rep.Matched) / float64(rep.Requests)
	}
	if sendDur > 0 {
		rep.SustainedReqPerS = round1(float64(rep.Requests) / sendDur.Seconds())
	}
	rep.MatchLatencyMs = percentiles(matchedMs)
	rep.OutcomeLatencyMs = percentiles(allMs)
	return rep
}

// listen opens the WebSocket and records the first outcome per request.
func (r *runner) listen(ctx context.Context) error {
	url := "ws" + strings.TrimPrefix(r.cfg.Target, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	conn.SetReadLimit(8 << 20)
	go func() {
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			now := time.Now()
			var env struct {
				Type string `json:"type"`
				Data struct {
					RequestID string `json:"request_id"`
					Reason    string `json:"reason"`
				} `json:"data"`
			}
			// Batched types (driver_moved, demand_hex, stats) carry arrays, which fail to
			// decode into this struct. That is fine: only matched/unmatched matter here.
			if json.Unmarshal(data, &env) != nil || (env.Type != "matched" && env.Type != "unmatched") {
				continue
			}
			r.mu.Lock()
			if _, seen := r.outcomes[env.Data.RequestID]; !seen {
				r.outcomes[env.Data.RequestID] = outcome{at: now, typ: env.Type, reason: env.Data.Reason}
			}
			r.mu.Unlock()
		}
	}()
	return nil
}

func (r *runner) post(ctx context.Context, path string, body any) (map[string]any, int, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.Target+path, bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(data, &out)
	return out, resp.StatusCode, nil
}

func (r *runner) sendRequest(ctx context.Context, riderID string, classIn int) {
	sentAt := time.Now()
	out, code, err := r.post(ctx, "/ride-requests", map[string]any{"rider_id": riderID, "class_in_min": classIn})
	if err != nil || code != http.StatusAccepted {
		r.reqErrs.Add(1)
		return
	}
	id, _ := out["request_id"].(string)
	r.mu.Lock()
	r.sent[id] = sentAt
	r.mu.Unlock()
}

// driveLoop moves one driver along their route, pinging every PingEvery.
func (r *runner) driveLoop(ctx context.Context, id string, d driverSpec) {
	kmPerPing := 40.0 * r.cfg.Speed * r.cfg.PingEvery.Seconds() / 3600
	pos := d.startKm
	t := time.NewTicker(r.cfg.PingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pos += kmPerPing
		if pos >= d.route.Length() {
			return // reached campus: stop pinging and drop off the map
		}
		p := d.route.At(pos)
		_, code, err := r.post(ctx, "/drivers/"+id+"/ping", map[string]any{"lat": p.Lat, "lng": p.Lng})
		if err != nil || code != http.StatusNoContent {
			if ctx.Err() == nil {
				r.pingErrs.Add(1)
			}
		}
	}
}

func registerAll(ctx context.Context, r *runner, path string, bodies []map[string]any) ([]string, error) {
	ids := make([]string, len(bodies))
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	var firstErr atomic.Value
	for i, b := range bodies {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out, code, err := r.post(ctx, path, b)
			if err != nil || code != http.StatusCreated {
				firstErr.CompareAndSwap(nil, fmt.Errorf("POST %s: status %d err %v body %v", path, code, err, out))
				return
			}
			ids[i], _ = out["id"].(string)
		}()
	}
	wg.Wait()
	if e := firstErr.Load(); e != nil {
		return nil, e.(error)
	}
	return ids, nil
}

func mapSlice[T any](in []T, f func(T) map[string]any) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

func pickGender(rng *rand.Rand) string {
	switch x := rng.Float64(); {
	case x < 0.45:
		return "woman"
	case x < 0.55:
		return "nonbinary"
	default:
		return "man"
	}
}

func makeSpecs(rng *rand.Rand, rts []*Route, cfg Config) ([]driverSpec, []riderSpec) {
	var drivers []driverSpec
	for i := 0; i < cfg.Drivers; i++ {
		rt := rts[rng.Intn(len(rts))]
		g := pickGender(rng)
		start := rng.Float64() * rt.Length() * 0.5
		p := rt.At(start)
		drivers = append(drivers, driverSpec{
			route: rt, startKm: start,
			body: map[string]any{
				"name": fmt.Sprintf("sim-driver-%d", i), "gender": g, "seats_total": 2 + rng.Intn(3),
				"route_id": rt.Name, "fuel_l_per_100km": 6.5 + rng.Float64()*3,
				"women_nb_only": g != "man" && rng.Float64() < 0.25, "lat": p.Lat, "lng": p.Lng,
			},
		})
	}
	var riders []riderSpec
	for i := 0; i < cfg.Riders; i++ {
		rt := rts[rng.Intn(len(rts))]
		p := rt.At(rt.Length() * (0.05 + 0.75*rng.Float64()))
		// Up to roughly 150 m off the route.
		p = geo.LatLng{Lat: p.Lat + (rng.Float64()-0.5)*0.0027, Lng: p.Lng + (rng.Float64()-0.5)*0.0027}
		riders = append(riders, riderSpec{
			classIn: classSlots[rng.Intn(len(classSlots))],
			body: map[string]any{
				"name": fmt.Sprintf("sim-rider-%d", i), "gender": pickGender(rng),
				"women_nb_only": rng.Float64() < 0.30, "lat": p.Lat, "lng": p.Lng,
			},
		})
	}
	return drivers, riders
}
