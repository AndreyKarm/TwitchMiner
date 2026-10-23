package twitch

import (
	"context"
	"sync"
	"time"
)

// RateLimiter allows at most Capacity operations to start within any
// Window, and at most Capacity to run at once.
type RateLimiter struct {
	capacity int
	window   time.Duration

	mu      sync.Mutex
	starts  []time.Time
	running int
}

// NewRateLimiter builds a limiter. GQL is volatile when rate limited, so the
// miner uses 5 requests per second.
func NewRateLimiter(capacity int, window time.Duration) *RateLimiter {
	return &RateLimiter{capacity: capacity, window: window}
}

// Acquire blocks until the operation may start. Call the returned function
// when it finishes.
func (r *RateLimiter) Acquire(ctx context.Context) (func(), error) {
	for {
		r.mu.Lock()
		now := time.Now()
		cut := 0
		for cut < len(r.starts) && now.Sub(r.starts[cut]) >= r.window {
			cut++
		}
		r.starts = r.starts[cut:]
		if len(r.starts) < r.capacity && r.running < r.capacity {
			r.starts = append(r.starts, now)
			r.running++
			r.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(func() {
					r.mu.Lock()
					r.running--
					r.mu.Unlock()
				})
			}, nil
		}
		wait := 20 * time.Millisecond
		if len(r.starts) >= r.capacity {
			wait = max(r.window-now.Sub(r.starts[0]), time.Millisecond)
		}
		r.mu.Unlock()
		if err := Sleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}
