package server

import (
	"context"
	"sync"
)

// Hub fans out state snapshots to SSE subscribers. Notify calls are coalesced:
// bursts of changes produce a single serialisation and broadcast.
type Hub struct {
	pending chan struct{}

	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

// NewHub creates an idle hub; call Run to start broadcasting.
func NewHub() *Hub {
	return &Hub{
		pending: make(chan struct{}, 1),
		clients: make(map[chan []byte]struct{}),
	}
}

// Notify schedules a broadcast. It never blocks.
func (h *Hub) Notify() {
	select {
	case h.pending <- struct{}{}:
	default:
	}
}

// Run broadcasts the payload produced by build after each Notify until ctx ends.
func (h *Hub) Run(ctx context.Context, build func() ([]byte, error)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.pending:
			payload, err := build()
			if err != nil {
				continue
			}
			h.mu.Lock()
			for ch := range h.clients {
				select {
				case ch <- payload:
				default: // slow client: drop this frame, it will get the next one
				}
			}
			h.mu.Unlock()
		}
	}
}

// Subscribe registers a client and returns its channel and an unsubscribe func.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 8)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
	}
}
