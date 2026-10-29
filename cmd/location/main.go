package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/location"
	"ainori/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		slog.Error("location service failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTrace, err := telemetry.Init(ctx, "ainori-location")
	if err != nil {
		return err
	}
	defer shutdownTrace(context.Background())

	rdb := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "localhost:6379")})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return err
	}

	store := location.NewStore(rdb, bus.NewRedisPublisher(rdb))
	go location.RunReaper(ctx, store, 5*time.Second)

	lis, err := net.Listen("tcp", env("LOCATION_ADDR", ":9091"))
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	pb.RegisterLocationServiceServer(srv, location.NewServer(store))

	go func() {
		<-ctx.Done()
		slog.Info("shutting down")
		srv.GracefulStop()
	}()
	slog.Info("location service listening", "addr", lis.Addr().String())
	return srv.Serve(lis)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
