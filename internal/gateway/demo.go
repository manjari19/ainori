package gateway

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// DemoHandler starts a light simulation so a visitor to the live map sees activity.
// Only one demo runs at a time, and one IP can start one per cooldown.
func DemoHandler(base context.Context, cooldown, maxRun time.Duration, run func(ctx context.Context) error) http.HandlerFunc {
	var mu sync.Mutex
	running := false
	lastByIP := map[string]time.Time{}

	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		mu.Lock()
		if running {
			mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "a demo is already running, watch the map"})
			return
		}
		if t, ok := lastByIP[ip]; ok && time.Since(t) < cooldown {
			mu.Unlock()
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "please wait a bit before starting another demo"})
			return
		}
		running = true
		lastByIP[ip] = time.Now()
		mu.Unlock()

		go func() {
			ctx, cancel := context.WithTimeout(base, maxRun)
			defer cancel()
			run(ctx)
			mu.Lock()
			running = false
			mu.Unlock()
		}()
		writeJSON(w, http.StatusAccepted, map[string]any{"started": true, "seconds": int(maxRun.Seconds())})
	}
}
