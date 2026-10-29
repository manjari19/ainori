package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/ids"
	"ainori/internal/location"
	"ainori/internal/matching"
	"ainori/internal/store"
)

type locAdapter struct{ srv *location.Server }

func (l locAdapter) UpdateLocation(ctx context.Context, in *pb.DriverPing, _ ...grpc.CallOption) (*pb.Ack, error) {
	return l.srv.UpdateLocation(ctx, in)
}
func (l locAdapter) NearbyDrivers(ctx context.Context, in *pb.NearbyRequest, _ ...grpc.CallOption) (*pb.DriverList, error) {
	return l.srv.NearbyDrivers(ctx, in)
}

type tripAdapter struct{ srv *matching.GRPCServer }

func (t tripAdapter) GetTrip(ctx context.Context, in *pb.TripID, _ ...grpc.CallOption) (*pb.Trip, error) {
	return t.srv.GetTrip(ctx, in)
}

type harness struct {
	ts  *httptest.Server
	rdb *redis.Client
	pg  *store.Postgres
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 13})
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis not reachable (run docker compose up -d): %v", err)
	}
	rdb.FlushDB(ctx)
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://ainori:ainori_dev_password@localhost:5433/ainori?sslmode=disable"
	}
	pg, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres not reachable: %v", err)
	}

	pub := bus.NewRedisPublisher(rdb)
	cons := bus.NewRedisConsumer(rdb)
	locStore := location.NewStore(rdb, pub)
	locSrv := location.NewServer(locStore)

	hub := NewHub([]string{"*"})
	agg := NewAggregator(hub, rdb)
	go cons.Subscribe(ctx, matching.StreamMatches, agg.OnMatch)
	go cons.Subscribe(ctx, location.StreamLocations, agg.OnLocation)
	go agg.Run(ctx)

	svc := matching.NewService(matching.Config{Group: "g-" + ids.New(), Consumer: "t", Workers: 2, ReclaimIdle: time.Second, GasPrice: 2},
		rdb, locAdapter{locSrv}, pg, pub, cons)
	go svc.Run(ctx)

	srv := NewServer(pg, locAdapter{locSrv}, tripAdapter{matching.NewGRPCServer(pg)}, pub, hub, agg, "*")
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(func() {
		ts.Close()
		cancel()
		time.Sleep(100 * time.Millisecond)
		bg := context.Background()
		pg.Pool().Exec(bg, `UPDATE ride_requests SET trip_id=NULL WHERE rider_id IN (SELECT id FROM riders WHERE name='gwtest')`)
		pg.Pool().Exec(bg, `DELETE FROM trips WHERE driver_id IN (SELECT id FROM drivers WHERE name='gwtest')`)
		pg.Pool().Exec(bg, `DELETE FROM ride_requests WHERE rider_id IN (SELECT id FROM riders WHERE name='gwtest')`)
		pg.Pool().Exec(bg, `DELETE FROM riders WHERE name='gwtest'`)
		pg.Pool().Exec(bg, `DELETE FROM drivers WHERE name='gwtest'`)
		pg.Close()
		rdb.FlushDB(bg)
		rdb.Close()
	})
	return &harness{ts: ts, rdb: rdb, pg: pg}
}

func (h *harness) post(t *testing.T, path string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(h.ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestRideRequestEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + h.ts.URL[len("http"):] + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	resp, d := h.post(t, "/drivers", map[string]any{"name": "gwtest", "gender": "woman", "seats_total": 2, "lat": 49.2380, "lng": -123.0020})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create driver: %d %v", resp.StatusCode, d)
	}
	resp, r := h.post(t, "/riders", map[string]any{"name": "gwtest", "gender": "woman", "lat": 49.2400, "lng": -123.0000})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create rider: %d %v", resp.StatusCode, r)
	}
	resp, rr := h.post(t, "/ride-requests", map[string]any{"rider_id": r["id"], "class_in_min": 180})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("ride request: %d %v", resp.StatusCode, rr)
	}

	var matched map[string]any
	for matched == nil {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no matched event over websocket: %v", err)
		}
		var env struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		json.Unmarshal(data, &env)
		if env.Type == "matched" {
			matched = env.Data
		}
	}
	if matched["request_id"] != rr["request_id"] || matched["driver_id"] != d["id"] {
		t.Fatalf("matched event %v does not match request %v / driver %v", matched, rr, d)
	}

	tripResp, err := http.Get(h.ts.URL + "/trips/" + matched["trip_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	defer tripResp.Body.Close()
	if tripResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /trips status %d", tripResp.StatusCode)
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		path string
		body map[string]any
		want int
	}{
		{"driver bad gender", "/drivers", map[string]any{"gender": "x", "seats_total": 2, "lat": 49.2, "lng": -123.0}, 400},
		{"driver zero seats", "/drivers", map[string]any{"gender": "man", "seats_total": 0, "lat": 49.2, "lng": -123.0}, 400},
		{"rider bad point", "/riders", map[string]any{"gender": "man", "lat": 200, "lng": 0}, 400},
		{"request without class time", "/ride-requests", map[string]any{"rider_id": ids.New()}, 400},
		{"request unknown rider", "/ride-requests", map[string]any{"rider_id": ids.New(), "class_in_min": 60}, 404},
		{"ping unknown driver", "/drivers/" + ids.New() + "/ping", map[string]any{"lat": 49.2, "lng": -123.0}, 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := h.post(t, tc.path, tc.body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	resp, _ := http.Get(h.ts.URL + "/trips/" + ids.New())
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown trip status %d, want 404", resp.StatusCode)
	}
}
