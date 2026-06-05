package sse

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: make(map[chan Event]struct{})}
}

func (h *Hub) Publish(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- e:
		default:
			// slow client; drop rather than block
		}
	}
}

func (h *Hub) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		ch := make(chan Event, 16)

		h.mu.Lock()
		h.clients[ch] = struct{}{}
		h.mu.Unlock()

		defer func() {
			h.mu.Lock()
			delete(h.clients, ch)
			close(ch)
			h.mu.Unlock()
		}()

		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-ch:
				data, err := json.Marshal(e.Payload)
				if err != nil {
					slog.Error("sse: marshal payload", "error", err)
					continue
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
				flusher.Flush()
			}
		}
	}
}
