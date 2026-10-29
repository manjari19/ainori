// Package location tracks where drivers are right now. Live state sits in Redis:
// a SET of driver IDs per H3 cell, a HASH per driver, and a ZSET of last-seen times.
package location

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/geo"
)

const (
	StreamLocations = "stream:locations"
	LastSeenKey     = "drivers:last_seen"
	StaleAfter      = 30 * time.Second
)

func CellKey(cell string) string { return "cell:" + cell }
func DriverKey(id string) string { return "driver:" + id }

type Store struct {
	rdb *redis.Client
	pub bus.Publisher
	now func() time.Time
}

func NewStore(rdb *redis.Client, pub bus.Publisher) *Store {
	return &Store{rdb: rdb, pub: pub, now: time.Now}
}

// Update records a driver ping. seats_left is only taken from the ping the first time
// we see a driver (HSETNX): after that the matcher owns it via the Lua seat claim, and a
// stale value in a later ping must not hand back a seat that was already given away.
func (s *Store) Update(ctx context.Context, p *pb.DriverPing) error {
	if p.DriverId == "" {
		return fmt.Errorf("driver_id is required")
	}
	cell, err := geo.CellOf(p.Lat, p.Lng)
	if err != nil {
		return err
	}
	key := DriverKey(p.DriverId)
	oldCell, err := s.rdb.HGet(ctx, key, "cell").Result()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("read old cell: %w", err)
	}
	now := s.now()
	eta := geo.TravelMin(geo.LatLng{Lat: p.Lat, Lng: p.Lng}, geo.Campus)

	pipe := s.rdb.TxPipeline()
	if oldCell != "" && oldCell != cell {
		pipe.SRem(ctx, CellKey(oldCell), p.DriverId)
	}
	pipe.SAdd(ctx, CellKey(cell), p.DriverId)
	pipe.HSetNX(ctx, key, "seats_left", p.SeatsLeft)
	pipe.HSetNX(ctx, key, "seats_total", p.SeatsLeft)
	pipe.HSet(ctx, key, map[string]any{
		"lat":              p.Lat,
		"lng":              p.Lng,
		"cell":             cell,
		"gender":           int32(p.Gender),
		"women_nb_only":    boolStr(p.WomenNbOnly),
		"eta_campus_min":   eta,
		"fuel_l_per_100km": p.FuelLPer_100Km,
		"last_seen":        now.Unix(),
	})
	pipe.ZAdd(ctx, LastSeenKey, redis.Z{Score: float64(now.Unix()), Member: p.DriverId})
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("write driver state: %w", err)
	}

	_, err = s.pub.Publish(ctx, StreamLocations, map[string]string{
		"driver_id": p.DriverId,
		"lat":       strconv.FormatFloat(p.Lat, 'f', 6, 64),
		"lng":       strconv.FormatFloat(p.Lng, 'f', 6, 64),
		"cell":      cell,
	})
	if err != nil {
		return fmt.Errorf("publish location: %w", err)
	}
	return nil
}

// Nearby searches the origin cell, then widens ring by ring up to maxK, and stops as soon
// as it has minResults drivers. It returns the full driver state so callers need no
// further Redis lookups.
func (s *Store) Nearby(ctx context.Context, cell string, maxK, minResults int) ([]*pb.Driver, int, error) {
	if maxK < 1 {
		maxK = 1
	}
	rings, err := geo.Rings(cell, maxK)
	if err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	var ids []string
	kUsed := 0
	for k := 0; k < len(rings); k++ {
		cmds := make([]*redis.StringSliceCmd, len(rings[k]))
		pipe := s.rdb.Pipeline()
		for i, c := range rings[k] {
			cmds[i] = pipe.SMembers(ctx, CellKey(c))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, 0, fmt.Errorf("read cells: %w", err)
		}
		for _, cmd := range cmds {
			for _, id := range cmd.Val() {
				if !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		kUsed = k
		// Ring 0 is the origin and ring 1 the first neighbours: k=1 is the first real search.
		if k >= 1 && len(ids) >= minResults {
			break
		}
	}
	if kUsed < 1 {
		kUsed = 1
	}
	drivers, err := s.loadDrivers(ctx, ids)
	return drivers, kUsed, err
}

func (s *Store) loadDrivers(ctx context.Context, ids []string) ([]*pb.Driver, error) {
	pipe := s.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGetAll(ctx, DriverKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("read drivers: %w", err)
	}
	out := make([]*pb.Driver, 0, len(ids))
	for i, cmd := range cmds {
		h := cmd.Val()
		if len(h) == 0 {
			continue // expired between the cell lookup and now
		}
		out = append(out, driverFromHash(ids[i], h))
	}
	return out, nil
}

func driverFromHash(id string, h map[string]string) *pb.Driver {
	f := func(k string) float64 { v, _ := strconv.ParseFloat(h[k], 64); return v }
	i := func(k string) int64 { v, _ := strconv.ParseInt(h[k], 10, 64); return v }
	return &pb.Driver{
		Id:             id,
		Lat:            f("lat"),
		Lng:            f("lng"),
		Cell:           h["cell"],
		SeatsLeft:      int32(i("seats_left")),
		Gender:         pb.Gender(i("gender")),
		WomenNbOnly:    h["women_nb_only"] == "1",
		EtaCampusMin:   f("eta_campus_min"),
		FuelLPer_100Km: f("fuel_l_per_100km"),
		LastSeenUnix:   i("last_seen"),
	}
}

// RemoveStale deletes drivers who have not pinged for StaleAfter and returns how many.
func (s *Store) RemoveStale(ctx context.Context) (int, error) {
	cutoff := strconv.FormatInt(s.now().Add(-StaleAfter).Unix(), 10)
	ids, err := s.rdb.ZRangeByScore(ctx, LastSeenKey, &redis.ZRangeBy{Min: "-inf", Max: cutoff}).Result()
	if err != nil {
		return 0, fmt.Errorf("find stale drivers: %w", err)
	}
	for _, id := range ids {
		cell, _ := s.rdb.HGet(ctx, DriverKey(id), "cell").Result()
		pipe := s.rdb.TxPipeline()
		if cell != "" {
			pipe.SRem(ctx, CellKey(cell), id)
		}
		pipe.Del(ctx, DriverKey(id))
		pipe.ZRem(ctx, LastSeenKey, id)
		if _, err := pipe.Exec(ctx); err != nil {
			return 0, fmt.Errorf("remove driver %s: %w", id, err)
		}
	}
	return len(ids), nil
}

// Online is how many drivers currently have live state.
func (s *Store) Online(ctx context.Context) (int64, error) {
	return s.rdb.ZCard(ctx, LastSeenKey).Result()
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
