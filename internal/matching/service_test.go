package matching

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/ids"
	"ainori/internal/location"
	"ainori/internal/store"
)

type env struct {
	rdb *redis.Client
	pg  *store.Postgres
	loc *location.Store
	svc *Service

	drivers, riders, requests []string
}

type inProcLocator struct{ srv *location.Server }

func (l inProcLocator) NearbyDrivers(ctx context.Context, in *pb.NearbyRequest, _ ...grpc.CallOption) (*pb.DriverList, error) {
	return l.srv.NearbyDrivers(ctx, in)
}

// newEnv wires the real matcher to real Redis (DB 14, wiped) and real Postgres.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 14})
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
		t.Fatalf("postgres not reachable (run docker compose up -d): %v", err)
	}
	pub := bus.NewRedisPublisher(rdb)
	locStore := location.NewStore(rdb, pub)
	e := &env{rdb: rdb, pg: pg, loc: locStore}
	e.svc = NewService(
		Config{Group: "g-" + ids.New(), Consumer: "test", Workers: 2, ReclaimIdle: 200 * time.Millisecond, GasPrice: 2.0},
		rdb, inProcLocator{location.NewServer(locStore)}, pg, pub, bus.NewRedisConsumer(rdb))
	t.Cleanup(func() {
		bg := context.Background()
		for _, id := range e.requests {
			pg.Pool().Exec(bg, `UPDATE ride_requests SET trip_id=NULL WHERE id=$1`, id)
		}
		pg.Pool().Exec(bg, `DELETE FROM trips WHERE driver_id = ANY($1::uuid[])`, e.drivers)
		pg.Pool().Exec(bg, `DELETE FROM ride_requests WHERE id = ANY($1::uuid[])`, e.requests)
		pg.Pool().Exec(bg, `DELETE FROM riders WHERE id = ANY($1::uuid[])`, e.riders)
		pg.Pool().Exec(bg, `DELETE FROM drivers WHERE id = ANY($1::uuid[])`, e.drivers)
		pg.Close()
		rdb.FlushDB(bg)
		rdb.Close()
	})
	return e
}

func (e *env) addDriver(t *testing.T, seats int32, lat, lng float64) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := e.pg.Pool().QueryRow(ctx,
		`INSERT INTO drivers (name, gender, seats_total, route_id) VALUES ('t','woman',$1,'r') RETURNING id::text`, seats).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	e.drivers = append(e.drivers, id)
	if err := e.loc.Update(ctx, &pb.DriverPing{DriverId: id, Lat: lat, Lng: lng, SeatsLeft: seats, Gender: pb.Gender_GENDER_WOMAN, FuelLPer_100Km: 8}); err != nil {
		t.Fatal(err)
	}
	return id
}

// addRequest creates the rider and the open request row, like the gateway will.
func (e *env) addRequest(t *testing.T, classIn time.Duration) Request {
	t.Helper()
	ctx := context.Background()
	var riderID string
	err := e.pg.Pool().QueryRow(ctx,
		`INSERT INTO riders (name, gender, pickup_lat, pickup_lng, class_start) VALUES ('r','woman',49.24,-123.0,'09:00') RETURNING id::text`).Scan(&riderID)
	if err != nil {
		t.Fatal(err)
	}
	e.riders = append(e.riders, riderID)
	r := Request{ID: ids.New(), RiderID: riderID, Lat: 49.2400, Lng: -123.0000,
		ClassStart: time.Now().Add(classIn), Gender: pb.Gender_GENDER_WOMAN, CreatedAt: time.Now()}
	_, err = e.pg.Pool().Exec(ctx,
		`INSERT INTO ride_requests (id, rider_id, pickup_lat, pickup_lng, class_start) VALUES ($1,$2,$3,$4,$5)`,
		r.ID, riderID, r.Lat, r.Lng, r.ClassStart)
	if err != nil {
		t.Fatal(err)
	}
	e.requests = append(e.requests, r.ID)
	return r
}

func (e *env) seatsLeft(t *testing.T, driverID string) int {
	t.Helper()
	n, err := e.rdb.HGet(context.Background(), location.DriverKey(driverID), "seats_left").Int()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) status(t *testing.T, reqID string) (status, reason string) {
	t.Helper()
	var r *string
	if err := e.pg.Pool().QueryRow(context.Background(), `SELECT status, reason FROM ride_requests WHERE id=$1`, reqID).Scan(&status, &r); err != nil {
		t.Fatal(err)
	}
	if r != nil {
		reason = *r
	}
	return
}

func (e *env) tripCount(t *testing.T, driverID string) int {
	t.Helper()
	var n int
	e.pg.Pool().QueryRow(context.Background(), `SELECT count(*) FROM trips WHERE driver_id=$1`, driverID).Scan(&n)
	return n
}

// 50 goroutines race for a 3-seat car: exactly 3 may win.
func TestClaimSeatConcurrency(t *testing.T) {
	e := newEnv(t)
	d := e.addDriver(t, 3, 49.2380, -123.0020)
	ctx := context.Background()

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, ok, err := ClaimSeat(ctx, e.rdb, d)
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 3 {
		t.Fatalf("%d claims succeeded, want exactly 3", wins.Load())
	}
	if left := e.seatsLeft(t, d); left != 0 {
		t.Fatalf("seats_left = %d, want 0 (never negative)", left)
	}
}

func TestClaimSeatStaleDriver(t *testing.T) {
	e := newEnv(t)
	_, _, ok, err := ClaimSeat(context.Background(), e.rdb, "no-such-driver")
	if err != nil || ok {
		t.Fatalf("claim on missing driver: ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestReleaseSeat(t *testing.T) {
	e := newEnv(t)
	d := e.addDriver(t, 2, 49.2380, -123.0020)
	ctx := context.Background()
	ClaimSeat(ctx, e.rdb, d)
	if err := ReleaseSeat(ctx, e.rdb, d); err != nil {
		t.Fatal(err)
	}
	if left := e.seatsLeft(t, d); left != 2 {
		t.Fatalf("seats_left = %d, want 2 after release", left)
	}
}

func TestProcessMatches(t *testing.T) {
	e := newEnv(t)
	d := e.addDriver(t, 2, 49.2380, -123.0020)
	r := e.addRequest(t, 3*time.Hour)

	if err := e.svc.Process(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.status(t, r.ID); st != "matched" {
		t.Fatalf("status = %s, want matched", st)
	}
	if e.tripCount(t, d) != 1 || e.seatsLeft(t, d) != 1 {
		t.Fatalf("trips=%d seats_left=%d, want 1 and 1", e.tripCount(t, d), e.seatsLeft(t, d))
	}
	msgs, _ := e.rdb.XRange(context.Background(), StreamMatches, "-", "+").Result()
	if len(msgs) != 1 {
		t.Fatalf("%d match events, want 1", len(msgs))
	}
	var ev Event
	json.Unmarshal([]byte(msgs[0].Values["payload"].(string)), &ev)
	if ev.Type != "matched" || ev.DriverID != d || ev.CostPerPerson <= 0 {
		t.Fatalf("bad event %+v", ev)
	}
}

// The same request delivered twice must make one trip and take one seat.
func TestProcessIsIdempotent(t *testing.T) {
	e := newEnv(t)
	d := e.addDriver(t, 3, 49.2380, -123.0020)
	r := e.addRequest(t, 3*time.Hour)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := e.svc.Process(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.tripCount(t, d); n != 1 {
		t.Fatalf("%d trips, want 1", n)
	}
	if left := e.seatsLeft(t, d); left != 2 {
		t.Fatalf("seats_left = %d, want 2 (one seat taken, not two)", left)
	}
	msgs, _ := e.rdb.XRange(ctx, StreamMatches, "-", "+").Result()
	if len(msgs) != 2 {
		t.Fatalf("%d events, want 2 (second delivery re-publishes the stored outcome)", len(msgs))
	}
	var a, b Event
	json.Unmarshal([]byte(msgs[0].Values["payload"].(string)), &a)
	json.Unmarshal([]byte(msgs[1].Values["payload"].(string)), &b)
	if a.TripID != b.TripID {
		t.Fatal("both events must carry the same trip id")
	}
}

// Five riders, one seat: one matched, four unmatched with no_seats, seat count never negative.
func TestLastSeatRace(t *testing.T) {
	e := newEnv(t)
	d := e.addDriver(t, 1, 49.2380, -123.0020)
	var reqs []Request
	for i := 0; i < 5; i++ {
		reqs = append(reqs, e.addRequest(t, 3*time.Hour))
	}
	var wg sync.WaitGroup
	for _, r := range reqs {
		wg.Add(1)
		go func(r Request) {
			defer wg.Done()
			if err := e.svc.Process(context.Background(), r); err != nil {
				t.Error(err)
			}
		}(r)
	}
	wg.Wait()
	matched := 0
	for _, r := range reqs {
		st, reason := e.status(t, r.ID)
		switch st {
		case "matched":
			matched++
		case "unmatched":
			if reason != string(ReasonNoSeats) {
				t.Errorf("unmatched reason = %s, want no_seats", reason)
			}
		default:
			t.Errorf("status %s", st)
		}
	}
	if matched != 1 || e.tripCount(t, d) != 1 || e.seatsLeft(t, d) != 0 {
		t.Fatalf("matched=%d trips=%d seats_left=%d, want 1, 1, 0", matched, e.tripCount(t, d), e.seatsLeft(t, d))
	}
}

func TestProcessUnmatchedReasons(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	r := e.addRequest(t, 3*time.Hour) // nobody online
	e.svc.Process(ctx, r)
	if _, reason := e.status(t, r.ID); reason != string(ReasonNoDriverNear) {
		t.Fatalf("reason = %s, want no_driver_nearby", reason)
	}

	e.addDriver(t, 2, 49.2380, -123.0020)
	late := e.addRequest(t, 8*time.Minute) // class too soon
	e.svc.Process(ctx, late)
	if _, reason := e.status(t, late.ID); reason != string(ReasonLate) {
		t.Fatalf("reason = %s, want late_for_class", reason)
	}
}

// A worker reads a request and dies without acknowledging. After the idle time another
// worker reclaims it and the rider still gets matched.
func TestCrashRecovery(t *testing.T) {
	e := newEnv(t)
	e.addDriver(t, 2, 49.2380, -123.0020)
	r := e.addRequest(t, 3*time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cons := bus.NewRedisConsumer(e.rdb)
	if err := cons.EnsureGroup(ctx, StreamRequests, e.svc.cfg.Group); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.NewRedisPublisher(e.rdb).Publish(ctx, StreamRequests, r.Fields()); err != nil {
		t.Fatal(err)
	}
	crashed, err := cons.Read(ctx, StreamRequests, e.svc.cfg.Group, "crashed-worker", 1, time.Second)
	if err != nil || len(crashed) != 1 {
		t.Fatalf("setup read: %v %d", err, len(crashed))
	}
	// "crashed-worker" never acks. Now the real service starts.
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, _ := e.status(t, r.ID); st == "matched" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, _ := e.status(t, r.ID); st != "matched" {
		t.Fatalf("status = %s, want matched after reclaim", st)
	}
	time.Sleep(100 * time.Millisecond)
	pending, _ := e.rdb.XPending(context.Background(), StreamRequests, e.svc.cfg.Group).Result()
	if pending.Count != 0 {
		t.Fatalf("%d messages still pending", pending.Count)
	}
	cancel()
	<-done
}

var _ = fmt.Sprintf
