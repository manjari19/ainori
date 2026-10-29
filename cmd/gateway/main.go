package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/bus"
	"ainori/internal/gateway"
	"ainori/internal/location"
	"ainori/internal/matching"
	"ainori/internal/sim"
	"ainori/internal/store"
	"ainori/internal/telemetry"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTrace, err := telemetry.Init(ctx, "ainori-gateway")
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

	creds := grpc.WithTransportCredentials(insecure.NewCredentials())
	tracing := grpc.WithStatsHandler(otelgrpc.NewClientHandler())
	locConn, err := grpc.NewClient(env("LOCATION_TARGET", "localhost:9091"), creds, tracing)
	if err != nil {
		return err
	}
	defer locConn.Close()
	matchConn, err := grpc.NewClient(env("MATCHING_TARGET", "localhost:9092"), creds, tracing)
	if err != nil {
		return err
	}
	defer matchConn.Close()

	cors := env("CORS_ORIGIN", "*")
	origins := []string{"*"}
	if cors != "*" {
		origins = []string{strings.TrimPrefix(strings.TrimPrefix(cors, "https://"), "http://")}
	}
	hub := gateway.NewHub(origins)
	agg := gateway.NewAggregator(hub, rdb)
	telemetry.RegisterDriversOnline(rdb, location.LastSeenKey)
	sub := bus.NewRedisConsumer(rdb)
	go sub.Subscribe(ctx, matching.StreamMatches, agg.OnMatch)
	go sub.Subscribe(ctx, location.StreamLocations, agg.OnLocation)
	go agg.Run(ctx)

	srv := gateway.NewServer(pg, pb.NewLocationServiceClient(locConn), pb.NewMatchingServiceClient(matchConn),
		bus.NewRedisPublisher(rdb), hub, agg, cors)
	addr := env("GATEWAY_ADDR", ":8080")
	srv.Demo = gateway.DemoHandler(ctx, time.Minute, 2*time.Minute, func(runCtx context.Context) error {
		// The demo drives this same gateway over HTTP, exactly like the load simulator.
		_, err := sim.Run(runCtx, sim.Config{
			Target: "http://127.0.0.1" + addr, Drivers: 40, Riders: 100, Rate: 1, Speed: 10, Seed: time.Now().UnixNano(), Drain: 5 * time.Second,
		})
		return err
	})
	httpSrv := &http.Server{Addr: addr, Handler: srv.Router(), ReadHeaderTimeout: 5 * time.Second}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()
	slog.Info("gateway listening", "addr", httpSrv.Addr)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
