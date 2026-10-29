package sim

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report is the saved result of one run. Every number in the README and on the resume
// has to come from one of these files.
type Report struct {
	StartedAt time.Time `json:"started_at"`
	Target    string    `json:"target"`
	Params    Params    `json:"params"`

	Requests     int            `json:"requests"`
	Matched      int            `json:"matched"`
	Unmatched    int            `json:"unmatched"`
	NoResponse   int            `json:"no_response"` // sent but no outcome arrived before the drain deadline
	RequestError int            `json:"request_errors"`
	PingErrors   int            `json:"ping_errors"`
	MatchRate    float64        `json:"match_rate"`
	Reasons      map[string]int `json:"unmatched_reasons"`

	MatchLatencyMs   Percentiles `json:"match_latency_ms"`   // matched requests only
	OutcomeLatencyMs Percentiles `json:"outcome_latency_ms"` // matched and unmatched
	SendSeconds      float64     `json:"send_seconds"`
	SustainedReqPerS float64     `json:"sustained_req_per_s"`
}

type Params struct {
	Drivers int     `json:"drivers"`
	Riders  int     `json:"riders"`
	Rate    float64 `json:"rate_req_per_s"`
	Speed   float64 `json:"speed_multiplier"`
	Seed    int64   `json:"seed"`
}

type Percentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func percentiles(ms []float64) Percentiles {
	if len(ms) == 0 {
		return Percentiles{}
	}
	s := append([]float64(nil), ms...)
	sort.Float64s(s)
	at := func(p float64) float64 { return s[int(float64(len(s)-1)*p/100+0.5)] }
	return Percentiles{P50: round1(at(50)), P95: round1(at(95)), P99: round1(at(99)), Max: round1(s[len(s)-1])}
}

func round1(x float64) float64 { return float64(int(x*10+0.5)) / 10 }

func (r *Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Requests: %d   Matched: %d (%.1f%%)   Unmatched: %d   No response: %d   Request errors: %d\n",
		r.Requests, r.Matched, r.MatchRate*100, r.Unmatched, r.NoResponse, r.RequestError)
	var parts []string
	keys := make([]string, 0, len(r.Reasons))
	for k := range r.Reasons {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, r.Reasons[k]))
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, "  %s\n", strings.Join(parts, " | "))
	}
	fmt.Fprintf(&b, "Match latency  p50 %.1fms  p95 %.1fms  p99 %.1fms  max %.1fms\n",
		r.MatchLatencyMs.P50, r.MatchLatencyMs.P95, r.MatchLatencyMs.P99, r.MatchLatencyMs.Max)
	fmt.Fprintf(&b, "Throughput     %.1f req/s sustained (target %.1f)   ping errors: %d\n",
		r.SustainedReqPerS, r.Params.Rate, r.PingErrors)
	return b.String()
}

// Save writes the report to dir/run-<timestamp>-r<rate>.json and returns the path.
func (r *Report) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("run-%s-rate%g.json", r.StartedAt.Format("20060102-150405"), r.Params.Rate)
	path := filepath.Join(dir, name)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}
