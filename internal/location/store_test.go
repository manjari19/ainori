package location

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/geo"
)

// testStore connects to real Redis on DB 15 and wipes it, so tests never touch dev data.
func testStore(t *testing.T) (*Store, *redis.Client) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis not reachable at %s (run docker compose up -d): %v", addr, err)
	}
	rdb.FlushDB(context.Background())
	t.Cleanup(func() { rdb.FlushDB(context.Background()); rdb.Close() })
	return NewStore(rdb, bus.NewRedisPublisher(rdb)), rdb
}

func ping(id string, lat, lng float64, seats int32) *pb.DriverPing {
	return &pb.DriverPing{DriverId: id, Lat: lat, Lng: lng, SeatsLeft: seats, FuelLPer_100Km: 8}
}

func TestUpdateMovesDriverBetweenCells(t *testing.T) {
	s, rdb := testStore(t)
	ctx := context.Background()
	if err := s.Update(ctx, ping("d1", 49.2276, -123.0076, 3)); err != nil {
		t.Fatal(err)
	}
	cellA, _ := geo.CellOf(49.2276, -123.0076)
	if !rdb.SIsMember(ctx, CellKey(cellA), "d1").Val() {
		t.Fatal("driver should be in its first cell")
	}
	// Move several km away: must leave the old cell set.
	if err := s.Update(ctx, ping("d1", 49.2500, -122.9500, 3)); err != nil {
		t.Fatal(err)
	}
	if rdb.SIsMember(ctx, CellKey(cellA), "d1").Val() {
		t.Fatal("driver should have left the old cell")
	}
	if n := rdb.XLen(ctx, StreamLocations).Val(); n != 2 {
		t.Fatalf("stream:locations has %d entries, want 2", n)
	}
}

func TestPingDoesNotResetSeats(t *testing.T) {
	s, rdb := testStore(t)
	ctx := context.Background()
	s.Update(ctx, ping("d1", 49.25, -122.95, 3))
	rdb.HSet(ctx, DriverKey("d1"), "seats_left", 1) // matcher claimed two seats
	s.Update(ctx, ping("d1", 49.25, -122.95, 3))    // stale ping still says 3
	if got := rdb.HGet(ctx, DriverKey("d1"), "seats_left").Val(); got != "1" {
		t.Fatalf("seats_left = %s, want 1", got)
	}
}

func TestNearbyWidensRingsAndRespectsMaxK(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	rider := geo.LatLng{Lat: 49.2500, Lng: -122.9500}
	cell, _ := geo.CellOf(rider.Lat, rider.Lng)

	// One driver close by, one roughly 2.5 km east (a few rings out), one far away.
	s.Update(ctx, ping("near", rider.Lat, rider.Lng+0.001, 2))
	s.Update(ctx, ping("mid", rider.Lat, rider.Lng+0.032, 2))
	s.Update(ctx, ping("far", rider.Lat, rider.Lng+0.30, 2))

	got, k, err := s.Nearby(ctx, cell, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Id != "near" || k != 1 {
		t.Fatalf("k=1 search: got %d drivers k=%d, want only 'near' with k=1", len(got), k)
	}

	got, k, _ = s.Nearby(ctx, cell, 5, 2)
	ids := map[string]bool{}
	for _, d := range got {
		ids[d.Id] = true
	}
	if !ids["near"] || !ids["mid"] || ids["far"] {
		t.Fatalf("wide search found %v, want near and mid but not far", ids)
	}
	if k < 2 {
		t.Fatalf("k used = %d, expected rings to widen past 1", k)
	}
}

func TestNearbyStopsOnceEnoughFound(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	cell, _ := geo.CellOf(49.25, -122.95)
	for i := 0; i < 5; i++ {
		s.Update(ctx, ping(fmt.Sprintf("d%d", i), 49.25, -122.95, 2))
	}
	s.Update(ctx, ping("outer", 49.25, -122.95+0.032, 2)) // a few rings away
	got, k, _ := s.Nearby(ctx, cell, 3, 5)
	if len(got) != 5 || k != 1 {
		t.Fatalf("got %d drivers at k=%d, want 5 at k=1 (no widening needed)", len(got), k)
	}
}

func TestRemoveStale(t *testing.T) {
	s, rdb := testStore(t)
	ctx := context.Background()
	base := time.Now()
	s.now = func() time.Time { return base.Add(-time.Minute) }
	s.Update(ctx, ping("old", 49.25, -122.95, 2))
	s.now = func() time.Time { return base }
	s.Update(ctx, ping("fresh", 49.30, -122.90, 2))

	n, err := s.RemoveStale(ctx)
	if err != nil || n != 1 {
		t.Fatalf("removed %d (err %v), want 1", n, err)
	}
	cell, _ := geo.CellOf(49.25, -122.95)
	if rdb.Exists(ctx, DriverKey("old")).Val() != 0 || rdb.SIsMember(ctx, CellKey(cell), "old").Val() {
		t.Fatal("stale driver state should be gone")
	}
	if rdb.Exists(ctx, DriverKey("fresh")).Val() != 1 {
		t.Fatal("fresh driver should remain")
	}
	if online, _ := s.Online(ctx); online != 1 {
		t.Fatalf("online = %d, want 1", online)
	}
}
