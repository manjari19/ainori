// Package store holds the Postgres repositories.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Pool() *pgxpool.Pool { return p.pool }
func (p *Postgres) Close()              { p.pool.Close() }

// Outcome is what is already recorded for a ride request, if anything.
type Outcome struct {
	Status string // open | matched | unmatched
	Event  []byte // the event JSON that was published
}

// RequestOutcome looks up a request. found is false if the request row does not exist.
func (p *Postgres) RequestOutcome(ctx context.Context, requestID string) (o Outcome, found bool, err error) {
	var event []byte
	err = p.pool.QueryRow(ctx,
		`SELECT status, outcome FROM ride_requests WHERE id = $1`, requestID).Scan(&o.Status, &event)
	if errors.Is(err, pgx.ErrNoRows) {
		return Outcome{}, false, nil
	}
	if err != nil {
		return Outcome{}, false, fmt.Errorf("select request %s: %w", requestID, err)
	}
	o.Event = event
	return o, true, nil
}

// Trip is the row written when a rider is matched.
type Trip struct {
	ID            string
	DriverID      string
	RiderIDs      []string
	EstArrival    time.Time
	DistanceKm    float64
	CostPerPerson float64
	PickupPoints  any
}

// ErrAlreadyDecided means another worker settled this request first.
var ErrAlreadyDecided = errors.New("request already decided")

// RecordMatch inserts the trip and marks the request matched in one transaction. The
// UPDATE only touches a still-open request, so two workers racing on the same
// duplicate message cannot both succeed.
func (p *Postgres) RecordMatch(ctx context.Context, requestID string, t Trip, event []byte) error {
	pts, err := json.Marshal(t.PickupPoints)
	if err != nil {
		return fmt.Errorf("encode pickup points: %w", err)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO trips (id, driver_id, rider_ids, est_arrival, distance_km, cost_per_person, pickup_points)
		 VALUES ($1, $2, $3::uuid[], $4, $5, $6, $7::jsonb)`,
		t.ID, t.DriverID, t.RiderIDs, t.EstArrival, t.DistanceKm, t.CostPerPerson, string(pts))
	if err != nil {
		return fmt.Errorf("insert trip: %w", err)
	}
	// If another worker already settled this request, 0 rows change and the rollback
	// discards the trip we just inserted.
	tag, err := tx.Exec(ctx,
		`UPDATE ride_requests SET status='matched', trip_id=$2, outcome=$3
		 WHERE id=$1 AND status='open'`, requestID, t.ID, event)
	if err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyDecided
	}
	return tx.Commit(ctx)
}

// RecordUnmatched marks a still-open request unmatched with a reason.
func (p *Postgres) RecordUnmatched(ctx context.Context, requestID string, reason string, event []byte) error {
	tag, err := p.pool.Exec(ctx,
		`UPDATE ride_requests SET status='unmatched', reason=$2, outcome=$3
		 WHERE id=$1 AND status='open'`, requestID, reason, event)
	if err != nil {
		return fmt.Errorf("update request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAlreadyDecided
	}
	return nil
}

// GetTrip loads one trip.
func (p *Postgres) GetTrip(ctx context.Context, id string) (Trip, bool, error) {
	var t Trip
	var ids []string
	err := p.pool.QueryRow(ctx,
		`SELECT id::text, driver_id::text, ARRAY(SELECT x::text FROM unnest(rider_ids) x), est_arrival, distance_km, cost_per_person::float8
		 FROM trips WHERE id = $1`, id).
		Scan(&t.ID, &t.DriverID, &ids, &t.EstArrival, &t.DistanceKm, &t.CostPerPerson)
	if errors.Is(err, pgx.ErrNoRows) {
		return Trip{}, false, nil
	}
	if err != nil {
		return Trip{}, false, fmt.Errorf("select trip %s: %w", id, err)
	}
	t.RiderIDs = ids
	return t, true, nil
}

// DriverRow is the registration data for a driver.
type DriverRow struct {
	ID          string
	Name        string
	Gender      string
	SeatsTotal  int
	RouteID     string
	Fuel        float64
	WomenNBOnly bool
}

func (p *Postgres) CreateDriver(ctx context.Context, d DriverRow) (string, error) {
	var id string
	err := p.pool.QueryRow(ctx,
		`INSERT INTO drivers (name, gender, seats_total, route_id, fuel_l_per_100km, women_nb_only)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`,
		d.Name, d.Gender, d.SeatsTotal, d.RouteID, d.Fuel, d.WomenNBOnly).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert driver: %w", err)
	}
	return id, nil
}

func (p *Postgres) GetDriver(ctx context.Context, id string) (DriverRow, bool, error) {
	d := DriverRow{ID: id}
	err := p.pool.QueryRow(ctx,
		`SELECT name, gender, seats_total, route_id, fuel_l_per_100km, women_nb_only FROM drivers WHERE id=$1`, id).
		Scan(&d.Name, &d.Gender, &d.SeatsTotal, &d.RouteID, &d.Fuel, &d.WomenNBOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return DriverRow{}, false, nil
	}
	if err != nil {
		return DriverRow{}, false, fmt.Errorf("select driver: %w", err)
	}
	return d, true, nil
}

// RiderRow is the registration data for a rider.
type RiderRow struct {
	ID          string
	Name        string
	Gender      string
	Lat, Lng    float64
	WomenNBOnly bool
}

func (p *Postgres) CreateRider(ctx context.Context, r RiderRow) (string, error) {
	var id string
	err := p.pool.QueryRow(ctx,
		`INSERT INTO riders (name, gender, pickup_lat, pickup_lng, class_start, women_nb_only)
		 VALUES ($1,$2,$3,$4,'09:00',$5) RETURNING id::text`,
		r.Name, r.Gender, r.Lat, r.Lng, r.WomenNBOnly).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert rider: %w", err)
	}
	return id, nil
}

func (p *Postgres) GetRider(ctx context.Context, id string) (RiderRow, bool, error) {
	r := RiderRow{ID: id}
	err := p.pool.QueryRow(ctx,
		`SELECT name, gender, pickup_lat, pickup_lng, women_nb_only FROM riders WHERE id=$1`, id).
		Scan(&r.Name, &r.Gender, &r.Lat, &r.Lng, &r.WomenNBOnly)
	if errors.Is(err, pgx.ErrNoRows) {
		return RiderRow{}, false, nil
	}
	if err != nil {
		return RiderRow{}, false, fmt.Errorf("select rider: %w", err)
	}
	return r, true, nil
}

// CreateRequest inserts the open ride request row the matcher will later settle.
func (p *Postgres) CreateRequest(ctx context.Context, id, riderID string, lat, lng float64, classStart time.Time) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO ride_requests (id, rider_id, pickup_lat, pickup_lng, class_start) VALUES ($1,$2,$3,$4,$5)`,
		id, riderID, lat, lng, classStart)
	if err != nil {
		return fmt.Errorf("insert request: %w", err)
	}
	return nil
}
