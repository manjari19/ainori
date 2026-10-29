package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ainori/internal/sim"
)

func main() {
	cfg := sim.Config{}
	flag.StringVar(&cfg.Target, "target", "http://localhost:8080", "gateway base URL")
	flag.IntVar(&cfg.Drivers, "drivers", 200, "number of simulated drivers")
	flag.IntVar(&cfg.Riders, "riders", 600, "number of ride requests to send")
	flag.Float64Var(&cfg.Rate, "rate", 10, "ride requests per second")
	flag.Float64Var(&cfg.Speed, "speed", 10, "how many times faster than real time drivers move")
	flag.Int64Var(&cfg.Seed, "seed", time.Now().UnixNano(), "random seed (fix it to repeat a run)")
	flag.DurationVar(&cfg.Drain, "drain", 10*time.Second, "how long to wait for outcomes after the last request")
	out := flag.String("out", "reports", "directory for the JSON report")
	flag.Parse()
	cfg.Logf = func(f string, a ...any) { log.Printf(f, a...) }

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rep, err := sim.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sim failed:", err)
		os.Exit(1)
	}
	fmt.Print(rep.Summary())
	path, err := rep.Save(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not save report:", err)
		os.Exit(1)
	}
	fmt.Println("report saved to", path)
}
