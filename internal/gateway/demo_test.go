package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDemoOneAtATimeAndRateLimited(t *testing.T) {
	release := make(chan struct{})
	h := DemoHandler(context.Background(), time.Minute, time.Minute, func(ctx context.Context) error {
		<-release
		return nil
	})
	call := func(addr string) int {
		req := httptest.NewRequest(http.MethodPost, "/demo/start", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}
	if c := call("1.1.1.1:1000"); c != http.StatusAccepted {
		t.Fatalf("first call %d, want 202", c)
	}
	if c := call("2.2.2.2:1000"); c != http.StatusConflict {
		t.Fatalf("second call while running %d, want 409", c)
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	if c := call("1.1.1.1:2000"); c != http.StatusTooManyRequests {
		t.Fatalf("same IP within cooldown %d, want 429", c)
	}
	if c := call("3.3.3.3:1000"); c != http.StatusAccepted {
		t.Fatalf("new IP after demo ended %d, want 202", c)
	}
}
