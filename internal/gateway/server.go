// Package gateway is the public face of Ainori: REST for commands, WebSocket for live updates.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/ids"
	"ainori/internal/matching"
	"ainori/internal/store"
	"ainori/internal/telemetry"
)

type LocationClient interface {
	UpdateLocation(ctx context.Context, in *pb.DriverPing, opts ...grpc.CallOption) (*pb.Ack, error)
}

type TripClient interface {
	GetTrip(ctx context.Context, in *pb.TripID, opts ...grpc.CallOption) (*pb.Trip, error)
}

type Repo interface {
	CreateDriver(ctx context.Context, d store.DriverRow) (string, error)
	GetDriver(ctx context.Context, id string) (store.DriverRow, bool, error)
	CreateRider(ctx context.Context, r store.RiderRow) (string, error)
	GetRider(ctx context.Context, id string) (store.RiderRow, bool, error)
	CreateRequest(ctx context.Context, id, riderID string, lat, lng float64, classStart time.Time) error
}

type Server struct {
	repo Repo
	loc  LocationClient
	trip TripClient
	pub  bus.Publisher
	hub  *Hub
	agg  *Aggregator

	// Small caches so a ping or a request does not hit Postgres every time.
	drivers sync.Map // id -> store.DriverRow
	riders  sync.Map // id -> store.RiderRow

	corsOrigin string
	Demo       http.HandlerFunc // set by the binary; nil means demo mode is off
}

func NewServer(repo Repo, loc LocationClient, trip TripClient, pub bus.Publisher, hub *Hub, agg *Aggregator, corsOrigin string) *Server {
	return &Server{repo: repo, loc: loc, trip: trip, pub: pub, hub: hub, agg: agg, corsOrigin: corsOrigin}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(s.cors)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	r.Handle("/metrics", promhttp.Handler())
	r.Get("/ws", s.hub.ServeWS)
	r.Get("/stats", s.handleStats)
	r.Post("/drivers", s.handleCreateDriver)
	r.Post("/drivers/{id}/ping", s.handlePing)
	r.Post("/riders", s.handleCreateRider)
	r.Post("/ride-requests", s.handleRideRequest)
	r.Get("/trips/{id}", s.handleGetTrip)
	r.Post("/demo/start", func(w http.ResponseWriter, r *http.Request) {
		if s.Demo == nil {
			http.Error(w, "demo mode is not enabled", http.StatusNotImplemented)
			return
		}
		s.Demo(w, r)
	})
	return r
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.corsOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type apiError struct {
	status int
	msg    string
}

func (e apiError) Error() string { return e.msg }

func badRequest(msg string) error { return apiError{http.StatusBadRequest, msg} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var ae apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.status, map[string]string{"error": ae.msg})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return badRequest("invalid JSON body")
	}
	return nil
}

func validGender(g string) bool { return g == "woman" || g == "man" || g == "nonbinary" }

func validPoint(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 && !(lat == 0 && lng == 0)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.agg.Stats(r.Context()))
}

type driverBody struct {
	Name        string  `json:"name"`
	Gender      string  `json:"gender"`
	SeatsTotal  int     `json:"seats_total"`
	RouteID     string  `json:"route_id"`
	Fuel        float64 `json:"fuel_l_per_100km"`
	WomenNBOnly bool    `json:"women_nb_only"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
}

func (s *Server) handleCreateDriver(w http.ResponseWriter, r *http.Request) {
	var b driverBody
	if err := decode(r, &b); err != nil {
		writeErr(w, err)
		return
	}
	if !validGender(b.Gender) || b.SeatsTotal < 1 || b.SeatsTotal > 6 || !validPoint(b.Lat, b.Lng) {
		writeErr(w, badRequest("need gender (woman|man|nonbinary), seats_total 1-6, and a valid lat/lng"))
		return
	}
	if b.Fuel <= 0 {
		b.Fuel = 8.0
	}
	if b.Name == "" {
		b.Name = "driver"
	}
	if b.RouteID == "" {
		b.RouteID = "unspecified"
	}
	row := store.DriverRow{Name: b.Name, Gender: b.Gender, SeatsTotal: b.SeatsTotal, RouteID: b.RouteID, Fuel: b.Fuel, WomenNBOnly: b.WomenNBOnly}
	id, err := s.repo.CreateDriver(r.Context(), row)
	if err != nil {
		writeErr(w, err)
		return
	}
	row.ID = id
	s.drivers.Store(id, row)
	if err := s.sendPing(r.Context(), row, b.Lat, b.Lng); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) sendPing(ctx context.Context, d store.DriverRow, lat, lng float64) error {
	_, err := s.loc.UpdateLocation(ctx, &pb.DriverPing{
		DriverId: d.ID, Lat: lat, Lng: lng, SeatsLeft: int32(d.SeatsTotal),
		Gender: matching.ParseGender(d.Gender), WomenNbOnly: d.WomenNBOnly, FuelLPer_100Km: d.Fuel,
	})
	return err
}

func (s *Server) driver(ctx context.Context, id string) (store.DriverRow, error) {
	if v, ok := s.drivers.Load(id); ok {
		return v.(store.DriverRow), nil
	}
	d, found, err := s.repo.GetDriver(ctx, id)
	if err != nil {
		return d, err
	}
	if !found {
		return d, apiError{http.StatusNotFound, "driver not found"}
	}
	s.drivers.Store(id, d)
	return d, nil
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	var b struct{ Lat, Lng float64 }
	if err := decode(r, &b); err != nil {
		writeErr(w, err)
		return
	}
	if !validPoint(b.Lat, b.Lng) {
		writeErr(w, badRequest("invalid lat/lng"))
		return
	}
	d, err := s.driver(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := s.sendPing(r.Context(), d, b.Lat, b.Lng); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateRider(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name        string  `json:"name"`
		Gender      string  `json:"gender"`
		Lat         float64 `json:"lat"`
		Lng         float64 `json:"lng"`
		WomenNBOnly bool    `json:"women_nb_only"`
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, err)
		return
	}
	if !validGender(b.Gender) || !validPoint(b.Lat, b.Lng) {
		writeErr(w, badRequest("need gender (woman|man|nonbinary) and a valid lat/lng"))
		return
	}
	if b.Name == "" {
		b.Name = "rider"
	}
	row := store.RiderRow{Name: b.Name, Gender: b.Gender, Lat: b.Lat, Lng: b.Lng, WomenNBOnly: b.WomenNBOnly}
	id, err := s.repo.CreateRider(r.Context(), row)
	if err != nil {
		writeErr(w, err)
		return
	}
	row.ID = id
	s.riders.Store(id, row)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) rider(ctx context.Context, id string) (store.RiderRow, error) {
	if v, ok := s.riders.Load(id); ok {
		return v.(store.RiderRow), nil
	}
	rr, found, err := s.repo.GetRider(ctx, id)
	if err != nil {
		return rr, err
	}
	if !found {
		return rr, apiError{http.StatusNotFound, "rider not found"}
	}
	s.riders.Store(id, rr)
	return rr, nil
}

// handleRideRequest saves the request, puts it on the stream, and answers right away.
// The match arrives later over the WebSocket.
func (s *Server) handleRideRequest(w http.ResponseWriter, r *http.Request) {
	ctx, span := telemetry.Tracer().Start(r.Context(), "gateway.ride_request")
	defer span.End()
	r = r.WithContext(ctx)

	var b struct {
		RiderID    string `json:"rider_id"`
		ClassStart string `json:"class_start"`  // RFC3339
		ClassInMin int    `json:"class_in_min"` // alternative: minutes from now
	}
	if err := decode(r, &b); err != nil {
		writeErr(w, err)
		return
	}
	var classStart time.Time
	switch {
	case b.ClassStart != "":
		t, err := time.Parse(time.RFC3339, b.ClassStart)
		if err != nil {
			writeErr(w, badRequest("class_start must be RFC3339, for example 2026-10-29T09:30:00-07:00"))
			return
		}
		classStart = t
	case b.ClassInMin > 0:
		classStart = time.Now().Add(time.Duration(b.ClassInMin) * time.Minute)
	default:
		writeErr(w, badRequest("provide class_start (RFC3339) or class_in_min"))
		return
	}
	if strings.TrimSpace(b.RiderID) == "" {
		writeErr(w, badRequest("rider_id is required"))
		return
	}
	rd, err := s.rider(r.Context(), b.RiderID)
	if err != nil {
		writeErr(w, err)
		return
	}
	req := matching.Request{
		ID: ids.New(), RiderID: rd.ID, Lat: rd.Lat, Lng: rd.Lng, ClassStart: classStart,
		Gender: matching.ParseGender(rd.Gender), WomenNBOnly: rd.WomenNBOnly, CreatedAt: time.Now(),
	}
	if err := s.repo.CreateRequest(r.Context(), req.ID, req.RiderID, req.Lat, req.Lng, classStart); err != nil {
		writeErr(w, err)
		return
	}
	if _, err := s.pub.Publish(r.Context(), matching.StreamRequests, req.Fields()); err != nil {
		writeErr(w, err)
		return
	}
	s.agg.RequestCreated(req.ID, req.Lat, req.Lng)
	writeJSON(w, http.StatusAccepted, map[string]string{"request_id": req.ID})
}

func (s *Server) handleGetTrip(w http.ResponseWriter, r *http.Request) {
	t, err := s.trip.GetTrip(r.Context(), &pb.TripID{Id: chi.URLParam(r, "id")})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			writeErr(w, apiError{http.StatusNotFound, "trip not found"})
			return
		}
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": t.Id, "driver_id": t.DriverId, "rider_ids": t.RiderIds,
		"est_arrival": t.EstArrival.AsTime(), "distance_km": t.DistanceKm, "cost_per_person": t.CostPerPerson,
	})
}
