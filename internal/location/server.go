package location

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "ainori/gen/ainori/v1"
)

// Server implements the LocationService gRPC API on top of a Store.
type Server struct {
	pb.UnimplementedLocationServiceServer
	store *Store
}

func NewServer(store *Store) *Server { return &Server{store: store} }

func (s *Server) UpdateLocation(ctx context.Context, p *pb.DriverPing) (*pb.Ack, error) {
	if err := s.store.Update(ctx, p); err != nil {
		return nil, status.Errorf(codes.Internal, "update location: %v", err)
	}
	return &pb.Ack{}, nil
}

func (s *Server) NearbyDrivers(ctx context.Context, r *pb.NearbyRequest) (*pb.DriverList, error) {
	if r.Cell == "" {
		return nil, status.Error(codes.InvalidArgument, "cell is required")
	}
	maxK, minRes := int(r.MaxK), int(r.MinResults)
	if maxK == 0 {
		maxK = 3
	}
	if minRes == 0 {
		minRes = 5
	}
	drivers, k, err := s.store.Nearby(ctx, r.Cell, maxK, minRes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "nearby drivers: %v", err)
	}
	return &pb.DriverList{Drivers: drivers, KUsed: int32(k)}, nil
}

// RunReaper clears stale drivers every interval until ctx is cancelled.
func RunReaper(ctx context.Context, s *Store, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.RemoveStale(ctx)
			if err != nil {
				slog.Error("reaper", "err", err)
			} else if n > 0 {
				slog.Info("removed stale drivers", "count", n)
			}
		}
	}
}
