package matching

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/cost"
	"ainori/internal/geo"
	"ainori/internal/ids"
	"ainori/internal/store"
	"ainori/internal/telemetry"
)

// Locator is the part of the location service the matcher needs. The generated gRPC
// client satisfies it.
type Locator interface {
	NearbyDrivers(ctx context.Context, in *pb.NearbyRequest, opts ...grpc.CallOption) (*pb.DriverList, error)
}

// Repository is the part of Postgres the matcher needs.
type Repository interface {
	RequestOutcome(ctx context.Context, requestID string) (store.Outcome, bool, error)
	RecordMatch(ctx context.Context, requestID string, t store.Trip, event []byte) error
	RecordUnmatched(ctx context.Context, requestID, reason string, event []byte) error
}

// Event is what the matcher publishes on stream:matches and stores as the outcome.
type Event struct {
	Type           string  `json:"type"` // matched | unmatched
	RequestID      string  `json:"request_id"`
	RiderID        string  `json:"rider_id"`
	Reason         string  `json:"reason,omitempty"`
	TripID         string  `json:"trip_id,omitempty"`
	DriverID       string  `json:"driver_id,omitempty"`
	DetourMin      float64 `json:"detour_min,omitempty"`
	PickupKm       float64 `json:"pickup_km,omitempty"`
	EstArrivalUnix int64   `json:"est_arrival_unix,omitempty"`
	ClassStartUnix int64   `json:"class_start_unix"`
	CostPerPerson  float64 `json:"cost_per_person,omitempty"`
	DistanceKm     float64 `json:"distance_km,omitempty"`
	RiderLat       float64 `json:"rider_lat"`
	RiderLng       float64 `json:"rider_lng"`
	DriverLat      float64 `json:"driver_lat,omitempty"`
	DriverLng      float64 `json:"driver_lng,omitempty"`
	CreatedUnixMs  int64   `json:"created_unix_ms"`
}

type Config struct {
	Group       string
	Consumer    string
	Workers     int
	ReclaimIdle time.Duration // how long a message may sit unacknowledged before another worker takes it
	GasPrice    float64
}

type Service struct {
	cfg  Config
	rdb  *redis.Client
	loc  Locator
	repo Repository
	pub  bus.Publisher
	cons bus.Consumer
	now  func() time.Time
}

func NewService(cfg Config, rdb *redis.Client, loc Locator, repo Repository, pub bus.Publisher, cons bus.Consumer) *Service {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	return &Service{cfg: cfg, rdb: rdb, loc: loc, repo: repo, pub: pub, cons: cons, now: time.Now}
}

// Run consumes ride requests until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	if err := s.cons.EnsureGroup(ctx, StreamRequests, s.cfg.Group); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < s.cfg.Workers; i++ {
		wg.Add(1)
		name := fmt.Sprintf("%s-%d", s.cfg.Consumer, i)
		go func() {
			defer wg.Done()
			s.worker(ctx, name)
		}()
	}
	wg.Wait()
	return nil
}

func (s *Service) worker(ctx context.Context, name string) {
	lastReclaim := time.Now()
	for ctx.Err() == nil {
		msgs, err := s.cons.Read(ctx, StreamRequests, s.cfg.Group, name, 5, 500*time.Millisecond)
		if err != nil && ctx.Err() == nil {
			slog.Error("read requests", "err", err)
			// Redis lost the stream or group (a flush or a restart without persistence): recreate it.
			if strings.Contains(err.Error(), "NOGROUP") {
				s.cons.EnsureGroup(ctx, StreamRequests, s.cfg.Group)
			}
			time.Sleep(time.Second)
		}
		// Messages another worker read but never acknowledged (it probably crashed)
		// are taken over here.
		if time.Since(lastReclaim) >= s.cfg.ReclaimIdle/2 {
			lastReclaim = time.Now()
			reclaimed, err := s.cons.Reclaim(ctx, StreamRequests, s.cfg.Group, name, s.cfg.ReclaimIdle, 10)
			if err != nil && ctx.Err() == nil {
				slog.Error("reclaim requests", "err", err)
			}
			msgs = append(msgs, reclaimed...)
		}
		for _, m := range msgs {
			s.handle(ctx, m)
		}
	}
}

// handle processes one message and acknowledges it only when the outcome is safely
// recorded. A failure leaves it pending so it is retried.
func (s *Service) handle(ctx context.Context, m bus.Message) {
	// Continue the trace the gateway started when it published this request.
	ctx = telemetry.Extract(ctx, m.Fields)
	ctx, span := telemetry.Tracer().Start(ctx, "matching.handle_request", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()

	req, err := ParseRequest(m.Fields)
	if err != nil {
		slog.Error("dropping malformed request", "id", m.ID, "err", err)
		s.ack(ctx, m.ID)
		return
	}
	if err := s.Process(ctx, req); err != nil {
		if errors.Is(err, errUnknownRequest) {
			slog.Error("dropping request with no database row", "request_id", req.ID)
			s.ack(ctx, m.ID)
			return
		}
		slog.Error("process request", "request_id", req.ID, "err", err)
		return
	}
	s.ack(ctx, m.ID)
}

func (s *Service) ack(ctx context.Context, id string) {
	if err := s.cons.Ack(ctx, StreamRequests, s.cfg.Group, id); err != nil {
		slog.Error("ack", "id", id, "err", err)
	}
}

var errUnknownRequest = errors.New("unknown request")

// Process matches one request. It is idempotent: if the request already has an outcome,
// that outcome is re-published instead of matching again, so redelivery is harmless.
func (s *Service) Process(ctx context.Context, req Request) error {
	o, found, err := s.repo.RequestOutcome(ctx, req.ID)
	if err != nil {
		return err
	}
	if !found {
		return errUnknownRequest
	}
	if o.Status != "open" {
		return s.publish(ctx, o.Event)
	}

	cell, err := geo.CellOf(req.Lat, req.Lng)
	if err != nil {
		return err
	}
	skipped := map[Reason]int{}
	// One re-search is allowed if every claim loses a race for the last seat.
	for attempt := 0; attempt < 2; attempt++ {
		list, err := s.loc.NearbyDrivers(ctx, &pb.NearbyRequest{Cell: cell, MaxK: 3, MinResults: 5})
		if err != nil {
			return fmt.Errorf("nearby drivers: %w", err)
		}
		var ranked []Candidate
		ranked, skipped = Rank(req, list.Drivers, s.now())
		if len(ranked) == 0 {
			return s.finishUnmatched(ctx, req, DominantReason(skipped))
		}
		for _, c := range ranked {
			claimCtx, claimSpan := telemetry.Tracer().Start(ctx, "redis.claim_seat_lua")
			left, total, ok, err := ClaimSeat(claimCtx, s.rdb, c.Driver.Id)
			claimSpan.SetAttributes(attribute.Bool("claimed", ok))
			claimSpan.End()
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			return s.finishMatched(ctx, req, c, total-left)
		}
	}
	return s.finishUnmatched(ctx, req, ReasonNoSeats)
}

func (s *Service) finishMatched(ctx context.Context, req Request, c Candidate, ridersOnTrip int) error {
	driver := geo.LatLng{Lat: c.Driver.Lat, Lng: c.Driver.Lng}
	rider := geo.LatLng{Lat: req.Lat, Lng: req.Lng}
	distance := (geo.Haversine(driver, rider) + geo.Haversine(rider, geo.Campus)) * 1.3
	perPerson := cost.PerPerson(distance, c.Driver.FuelLPer_100Km, s.cfg.GasPrice, ridersOnTrip)

	tripID := ids.New()
	ev := Event{
		Type: "matched", RequestID: req.ID, RiderID: req.RiderID, TripID: tripID,
		DriverID: c.Driver.Id, DetourMin: round2(c.DetourMin), PickupKm: round2(c.PickupKm),
		EstArrivalUnix: c.EstArrival.Unix(), ClassStartUnix: req.ClassStart.Unix(),
		CostPerPerson: perPerson, DistanceKm: round2(distance),
		RiderLat: req.Lat, RiderLng: req.Lng, DriverLat: c.Driver.Lat, DriverLng: c.Driver.Lng,
		CreatedUnixMs: req.CreatedAt.UnixMilli(),
	}
	payload, _ := json.Marshal(ev)
	dbCtx, dbSpan := telemetry.Tracer().Start(ctx, "postgres.record_match")
	err := s.repo.RecordMatch(dbCtx, req.ID, store.Trip{
		ID: tripID, DriverID: c.Driver.Id, RiderIDs: []string{req.RiderID},
		EstArrival: c.EstArrival, DistanceKm: distance, CostPerPerson: perPerson,
		PickupPoints: []geo.LatLng{rider},
	}, payload)
	dbSpan.End()
	if err != nil {
		// The seat was taken in Redis but the trip was not saved: give it back so it is not lost.
		if relErr := ReleaseSeat(ctx, s.rdb, c.Driver.Id); relErr != nil {
			slog.Error("release seat after failed save", "err", relErr)
		}
		if errors.Is(err, store.ErrAlreadyDecided) {
			return s.republishStored(ctx, req.ID)
		}
		return fmt.Errorf("record match: %w", err)
	}
	telemetry.Matches.Inc()
	observeLatency(req)
	return s.publish(ctx, payload)
}

func (s *Service) finishUnmatched(ctx context.Context, req Request, reason Reason) error {
	ev := Event{
		Type: "unmatched", RequestID: req.ID, RiderID: req.RiderID, Reason: string(reason),
		ClassStartUnix: req.ClassStart.Unix(), RiderLat: req.Lat, RiderLng: req.Lng,
		CreatedUnixMs: req.CreatedAt.UnixMilli(),
	}
	payload, _ := json.Marshal(ev)
	if err := s.repo.RecordUnmatched(ctx, req.ID, string(reason), payload); err != nil {
		if errors.Is(err, store.ErrAlreadyDecided) {
			return s.republishStored(ctx, req.ID)
		}
		return fmt.Errorf("record unmatched: %w", err)
	}
	telemetry.Unmatched.WithLabelValues(string(reason)).Inc()
	observeLatency(req)
	return s.publish(ctx, payload)
}

func (s *Service) republishStored(ctx context.Context, requestID string) error {
	o, _, err := s.repo.RequestOutcome(ctx, requestID)
	if err != nil {
		return err
	}
	return s.publish(ctx, o.Event)
}

func (s *Service) publish(ctx context.Context, payload []byte) error {
	var head struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return fmt.Errorf("decode stored outcome: %w", err)
	}
	_, err := s.pub.Publish(ctx, StreamMatches, map[string]string{
		"type": head.Type, "request_id": head.RequestID, "payload": string(payload),
	})
	return err
}

func observeLatency(req Request) {
	if !req.CreatedAt.IsZero() {
		telemetry.MatchLatency.Observe(time.Since(req.CreatedAt).Seconds())
	}
}

func round2(x float64) float64 { return float64(int(x*100+0.5)) / 100 }
