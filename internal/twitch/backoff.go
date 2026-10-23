// Package twitch talks to Twitch: HTTP with retries, OAuth device-code
// login and the GraphQL API.
package twitch

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

// Backoff yields exponentially growing delays with ±10% jitter, capped at Max.
type Backoff struct {
	Base  float64
	Max   time.Duration
	steps int
}

// NewBackoff returns a base-2 backoff capped at max.
func NewBackoff(max time.Duration) *Backoff {
	return &Backoff{Base: 2, Max: max}
}

// Steps reports how many delays have been handed out since the last reset.
func (b *Backoff) Steps() int { return b.steps }

// Next returns the next delay.
func (b *Backoff) Next() time.Duration {
	seconds := math.Pow(b.Base, float64(b.steps)) * (0.9 + rand.Float64()*0.2)
	d := time.Duration(seconds * float64(time.Second))
	if d > b.Max {
		return b.Max
	}
	b.steps++
	return d
}

// Reset starts the sequence over.
func (b *Backoff) Reset() { b.steps = 0 }

// Sleep waits for d or until ctx is done, returning ctx.Err() in that case.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
