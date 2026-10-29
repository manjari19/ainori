package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type client struct {
	send chan []byte
}

// Hub fans messages out to every connected browser. A client that cannot keep up is
// dropped rather than allowed to slow everyone else down.
type Hub struct {
	mu      sync.Mutex
	clients map[*client]struct{}
	origins []string
}

func NewHub(originPatterns []string) *Hub {
	return &Hub{clients: map[*client]struct{}{}, origins: originPatterns}
}

func (h *Hub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			delete(h.clients, c)
			close(c.send)
		}
	}
}

func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.origins})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	c := &client{send: make(chan []byte, 512)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if _, ok := h.clients[c]; ok {
			delete(h.clients, c)
			close(c.send)
		}
		h.mu.Unlock()
	}()

	// CloseRead drains incoming frames and cancels ctx when the browser goes away.
	ctx := conn.CloseRead(r.Context())
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				conn.Close(websocket.StatusPolicyViolation, "too slow")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
