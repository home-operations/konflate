package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/home-operations/konflate/internal/api"
)

// hub fans diff-job status events out to every connected websocket client so
// the UI updates live without polling.
type hub struct {
	log     *slog.Logger
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	send chan []byte
}

func newHub(log *slog.Logger) *hub {
	return &hub{log: log, clients: map[*wsClient]struct{}{}}
}

// broadcast marshals ev and delivers it to every client. A client whose buffer
// is full is skipped (the UI reconciles via a full refresh on reconnect), so a
// single slow consumer never blocks job progress.
func (h *hub) broadcast(ev api.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
		}
	}
}

func (h *hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *hub) remove(c *wsClient) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// serveWS upgrades the connection and pumps events until the client
// disconnects or the request context is cancelled (server shutdown).
func (h *hub) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
	if err != nil {
		h.log.Warn("websocket accept failed", "error", err)
		return
	}
	c := &wsClient{send: make(chan []byte, 32)}
	h.add(c)
	defer func() {
		h.remove(c)
		_ = conn.CloseNow()
	}()

	// CloseRead drains client frames (handling pings/close) in the background
	// and yields a context cancelled when the peer goes away.
	ctx := conn.CloseRead(r.Context())

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
