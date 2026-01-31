package server

import (
	"encoding/json"
	"net/http"
	"sync"
)

type SSEHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func NewSSEHub() *SSEHub {
	return &SSEHub{clients: make(map[chan []byte]struct{}, 8)}
}

func (h *SSEHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 64)

	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
		close(ch)
	}()

	// initial ping
	w.Write([]byte("event: ping\ndata: {}\n\n"))
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			w.Write([]byte("event: event\n"))
			w.Write([]byte("data: "))
			w.Write(b)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

func (h *SSEHub) Broadcast(v any) {
	b, _ := json.Marshal(v)

	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.clients {
		select {
		case ch <- b:
		default:
			// drop if client is slow
		}
	}
}
