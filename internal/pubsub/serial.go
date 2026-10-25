package pubsub

import (
	"log/slog"
	"runtime/debug"
	"sync"
)

// serial runs functions one at a time per key, in the order queued, while
// different keys run in parallel. A goroutine exists for a key only while
// it has work, so idle topics cost nothing.
type serial struct {
	mu sync.Mutex
	// queues holds the waiting work per key. A key is present exactly while
	// a goroutine is draining it.
	queues map[string][]func()
}

func (s *serial) run(key string, fn func()) {
	s.mu.Lock()
	if s.queues == nil {
		s.queues = map[string][]func(){}
	}
	q, draining := s.queues[key]
	s.queues[key] = append(q, fn)
	s.mu.Unlock()
	if !draining {
		go s.drain(key)
	}
}

func (s *serial) drain(key string) {
	for {
		s.mu.Lock()
		q := s.queues[key]
		if len(q) == 0 {
			delete(s.queues, key)
			s.mu.Unlock()
			return
		}
		fn := q[0]
		s.queues[key] = q[1:]
		s.mu.Unlock()
		runRecovered(key, fn)
	}
}

// runRecovered keeps a panicking handler from killing the drain goroutine,
// and with it the process. Handlers should recover themselves; this is the
// backstop.
func runRecovered(key string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("pubsub handler panicked", "topic", key, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn()
}
