package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/matching"
	"ainori/internal/store"
	"ainori/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		slog.Error("matching service failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTrace, err := telemetry.Init(ctx, "ainori-matching")
	if err != nil {
		return err
	}
	defer shutdownTrace(context.Background())

	rdb := redis.NewClient(&redis.Options{Addr: env("REDIS_ADDR", "localhost:6379")})
	defer rdb.Close()
	pg, err := store.NewPostgres(ctx, env("DATABASE_URL", "postgres://ainori:ainori_dev_password@localhost:5433/ainori?sslmode=disable"))
	if err != nil {
		return err
	}
	defer pg.Close()

	conn, err := grpc.NewClient(env("LOCATION_TARGET", "localhost:9091"), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		return err
	}
	defer conn.Close()

	gas, _ := strconv.ParseFloat(env("GAS_PRICE_PER_L", "1.85"), 64)
	host, _ := os.Hostname()
	svc := matching.NewService(matching.Config{
		Group: "matchers", Consumer: host, Workers: 8, ReclaimIdle: 30 * time.Second, GasPrice: gas,
	}, rdb, pb.NewLocationServiceClient(conn), pg, bus.NewRedisPublisher(rdb), bus.NewRedisConsumer(rdb))

	lis, err := net.Listen("tcp", env("MATCHING_ADDR", ":9092"))
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	pb.RegisterMatchingServiceServer(srv, matching.NewGRPCServer(pg))
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	go srv.Serve(lis)

	go telemetry.WatchPending(ctx, rdb, matching.StreamRequests, "matchers")
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	metricsSrv := &http.Server{Addr: env("MATCHING_METRICS_ADDR", ":9102"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go metricsSrv.ListenAndServe()
	go func() {
		<-ctx.Done()
		metricsSrv.Close()
	}()

	slog.Info("matching service running", "grpc", lis.Addr().String())
	return svc.Run(ctx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
