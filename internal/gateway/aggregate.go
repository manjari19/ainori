package gateway

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/trace"

	"ainori/internal/bus"
	"ainori/internal/geo"
	"ainori/internal/location"
	"ainori/internal/matching"
	"ainori/internal/telemetry"
)

const (
	batchEvery    = 250 * time.Millisecond
	latencyWindow = 1000
)

// Stats are the live counters shown in the map's side panel.
type Stats struct {
	DriversOnline int64          `json:"drivers_online"`
	OpenRequests  int            `json:"open_requests"`
	Matched       int            `json:"matched"`
	Unmatched     int            `json:"unmatched"`
	MatchRate     float64        `json:"match_rate"`
	Reasons       map[string]int `json:"unmatched_reasons"`
	P95Ms         float64        `json:"p95_ms"`
	P99Ms         float64        `json:"p99_ms"`
}

// Aggregator turns raw stream events into browser messages. Matches go out immediately
// so latency stays honest. Driver moves and demand counts are batched every 250ms so
// the browser is not flooded.
type Aggregator struct {
	hub *Hub
	rdb *redis.Client

	mu        sync.Mutex
	moved     map[string]map[string]any // driver id -> latest position this window
	demand    map[string]int            // open requests per hex
	dirty     map[string]bool
	created   map[string]int // requests created per hex since the last flush
	matched   int
	unmatched int
	reasons   map[string]int
	latencies []float64 // ms, most recent window
}

func NewAggregator(hub *Hub, rdb *redis.Client) *Aggregator {
	return &Aggregator{
		hub: hub, rdb: rdb,
		moved: map[string]map[string]any{}, demand: map[string]int{}, dirty: map[string]bool{}, created: map[string]int{},
		reasons: map[string]int{},
	}
}

func envelope(typ string, data any) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "data": data})
	return b
}

// RequestCreated is called by the REST handler the moment a request is accepted.
func (a *Aggregator) RequestCreated(requestID string, lat, lng float64) {
	cell, err := geo.CellOf(lat, lng)
	if err != nil {
		return
	}
	a.mu.Lock()
	a.demand[cell]++
	a.created[cell]++
	a.dirty[cell] = true
	a.mu.Unlock()
	a.hub.Broadcast(envelope("request_created", map[string]any{
		"request_id": requestID, "lat": lat, "lng": lng, "cell": cell,
	}))
}

func (a *Aggregator) OnMatch(m bus.Message) {
	_, span := telemetry.Tracer().Start(telemetry.Extract(context.Background(), m.Fields), "gateway.push_to_websocket",
		trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	var ev matching.Event
	if err := json.Unmarshal([]byte(m.Fields["payload"]), &ev); err != nil {
		return
	}
	a.mu.Lock()
	if cell, err := geo.CellOf(ev.RiderLat, ev.RiderLng); err == nil && a.demand[cell] > 0 {
		a.demand[cell]--
		a.dirty[cell] = true
	}
	if ev.Type == "matched" {
		a.matched++
	} else {
		a.unmatched++
		a.reasons[ev.Reason]++
	}
	if ev.CreatedUnixMs > 0 {
		a.latencies = append(a.latencies, float64(time.Now().UnixMilli()-ev.CreatedUnixMs))
		if len(a.latencies) > latencyWindow {
			a.latencies = a.latencies[len(a.latencies)-latencyWindow:]
		}
	}
	a.mu.Unlock()
	a.hub.Broadcast(envelope(ev.Type, ev))
}

func (a *Aggregator) OnLocation(m bus.Message) {
	lat, lng := m.Fields["lat"], m.Fields["lng"]
	a.mu.Lock()
	a.moved[m.Fields["driver_id"]] = map[string]any{
		"id": m.Fields["driver_id"], "lat": lat, "lng": lng, "cell": m.Fields["cell"],
	}
	a.mu.Unlock()
}

func (a *Aggregator) Stats(ctx context.Context) Stats {
	online, _ := a.rdb.ZCard(ctx, location.LastSeenKey).Result()
	a.mu.Lock()
	defer a.mu.Unlock()
	open := 0
	for _, n := range a.demand {
		open += n
	}
	s := Stats{DriversOnline: online, OpenRequests: open, Matched: a.matched, Unmatched: a.unmatched,
		Reasons: map[string]int{}}
	for k, v := range a.reasons {
		s.Reasons[k] = v
	}
	if total := a.matched + a.unmatched; total > 0 {
		s.MatchRate = float64(a.matched) / float64(total)
	}
	s.P95Ms = percentile(a.latencies, 95)
	s.P99Ms = percentile(a.latencies, 99)
	return s
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	i := int(float64(len(c)-1) * p / 100)
	return c[i]
}

// Run flushes batched updates every 250ms until ctx is cancelled.
func (a *Aggregator) Run(ctx context.Context) {
	t := time.NewTicker(batchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.flush(ctx)
		}
	}
}

func (a *Aggregator) flush(ctx context.Context) {
	a.mu.Lock()
	var moved []map[string]any
	for _, d := range a.moved {
		moved = append(moved, d)
	}
	a.moved = map[string]map[string]any{}
	var demand []map[string]any
	for cell := range a.dirty {
		// count is open requests now. recent is requests created since the last batch:
		// matching takes milliseconds, so open requests alone is almost always zero.
		demand = append(demand, map[string]any{"cell": cell, "count": a.demand[cell], "recent": a.created[cell]})
		if a.demand[cell] == 0 {
			delete(a.demand, cell)
		}
	}
	a.dirty = map[string]bool{}
	a.created = map[string]int{}
	a.mu.Unlock()

	if len(moved) > 0 {
		a.hub.Broadcast(envelope("driver_moved", moved))
	}
	if len(demand) > 0 {
		a.hub.Broadcast(envelope("demand_hex", demand))
	}
	if a.hub.Clients() > 0 {
		a.hub.Broadcast(envelope("stats", a.Stats(ctx)))
	}
}
