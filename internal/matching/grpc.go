package matching

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/store"
)

type TripGetter interface {
	GetTrip(ctx context.Context, id string) (store.Trip, bool, error)
}

// GRPCServer serves MatchingService.GetTrip.
type GRPCServer struct {
	pb.UnimplementedMatchingServiceServer
	trips TripGetter
}

func NewGRPCServer(trips TripGetter) *GRPCServer { return &GRPCServer{trips: trips} }

func (g *GRPCServer) GetTrip(ctx context.Context, id *pb.TripID) (*pb.Trip, error) {
	t, found, err := g.trips.GetTrip(ctx, id.Id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get trip: %v", err)
	}
	if !found {
		return nil, status.Error(codes.NotFound, "trip not found")
	}
	return &pb.Trip{
		Id: t.ID, DriverId: t.DriverID, RiderIds: t.RiderIDs,
		EstArrival: timestamppb.New(t.EstArrival), DistanceKm: t.DistanceKm, CostPerPerson: t.CostPerPerson,
	}, nil
}
