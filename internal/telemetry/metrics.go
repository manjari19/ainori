package telemetry

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/redis/go-redis/v9"
)

var (
	MatchLatency = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ainori_match_latency_seconds",
		Help:    "Time from a ride request being created to its outcome being recorded.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})
	Matches = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ainori_matches_total", Help: "Riders matched to a driver.",
	})
	Unmatched = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ainori_unmatched_total", Help: "Riders left unmatched, by reason.",
	}, []string{"reason"})
	StreamPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ainori_stream_pending", Help: "Messages read but not yet acknowledged, by stream.",
	}, []string{"stream"})
)

// RegisterDriversOnline exposes how many drivers currently have live state in Redis.
func RegisterDriversOnline(rdb *redis.Client, lastSeenKey string) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "ainori_drivers_online", Help: "Drivers with live state in Redis.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		n, err := rdb.ZCard(ctx, lastSeenKey).Result()
		if err != nil {
			return 0
		}
		return float64(n)
	})
}

// WatchPending refreshes the pending-messages gauge for a consumer group until ctx ends.
func WatchPending(ctx context.Context, rdb *redis.Client, stream, group string) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p, err := rdb.XPending(ctx, stream, group).Result(); err == nil {
				StreamPending.WithLabelValues(stream).Set(float64(p.Count))
			}
		}
	}
}
