// Package web serves the dashboard and streams miner state to it over
// Server-Sent Events.
package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const consoleHistory = 300

type consoleLine struct {
	Time int64  `json:"time"`
	Text string `json:"text"`
}

// Hub keeps the latest value of every dashboard section and fans updates
// out to connected browsers. It implements miner.UI.
type Hub struct {
	mu       sync.Mutex
	sections map[string][]byte
	order    []string
	console  []consoleLine
	clients  map[chan []byte]struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{sections: map[string][]byte{}, clients: map[chan []byte]struct{}{}}
}

// Publish replaces a section and broadcasts it.
func (h *Hub) Publish(section string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("encoding dashboard section", "section", section, "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.sections[section]; !ok {
		h.order = append(h.order, section)
	}
	h.sections[section] = data
	h.broadcastLocked(frame(section, data))
}

// Print appends a console line and broadcasts it.
func (h *Hub) Print(text string) {
	line := consoleLine{Time: time.Now().UnixMilli(), Text: text}
	data, _ := json.Marshal(line)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.console = append(h.console, line)
	if len(h.console) > consoleHistory {
		h.console = h.console[len(h.console)-consoleHistory:]
	}
	h.broadcastLocked(frame("console", data))
}

// subscribe registers a client and returns its channel plus the frames
// that bring it up to date.
func (h *Hub) subscribe() (chan []byte, [][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan []byte, 256)
	h.clients[ch] = struct{}{}
	snapshot := make([][]byte, 0, len(h.order)+1)
	for _, name := range h.order {
		snapshot = append(snapshot, frame(name, h.sections[name]))
	}
	history, _ := json.Marshal(h.console)
	snapshot = append(snapshot, frame("console_history", history))
	return ch, snapshot
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
}

func (h *Hub) broadcastLocked(f []byte) {
	for ch := range h.clients {
		select {
		case ch <- f:
		default:
			// A client this far behind reconnects and gets a fresh snapshot.
			delete(h.clients, ch)
			close(ch)
		}
	}
}

func frame(event string, data []byte) []byte {
	return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, data)
}
