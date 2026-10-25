package miner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/twitch"
)

// A panic on any goroutine ends the whole process, so every goroutine the
// miner starts recovers from one. Recovering is only safe if the panic
// didn't leave m.mu locked, which would hang the miner instead; Recovered
// checks that and exits when it did.

var (
	// lockLeakWait is how long Recovered waits for m.mu. Nothing does I/O
	// while holding it, so a lock still held after this was leaked by the
	// panic.
	lockLeakWait = 5 * time.Second
	// exitProcess ends the process; tests replace it.
	exitProcess = os.Exit
)

// Recovered logs a recovered panic and exits if it left the miner locked.
func (m *Miner) Recovered(where string, r any) {
	slog.Error("recovered from an internal error", "in", where, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	m.print(fmt.Sprintf("Internal error in %s; recovered. Details are in the log.", where))
	if !m.lockFree(lockLeakWait) {
		slog.Error("the internal error left the miner locked; exiting so it can be restarted")
		exitProcess(1)
	}
}

// lockFree reports whether m.mu can be taken within wait.
func (m *Miner) lockFree(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for !m.mu.TryLock() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Unlock()
	return true
}

// recoverPanic is deferred at the top of a goroutine.
func (m *Miner) recoverPanic(where string) {
	if r := recover(); r != nil {
		m.Recovered(where, r)
	}
}

// goSafe runs fn on a new goroutine that survives a panic.
func (m *Miner) goSafe(where string, fn func()) {
	go func() {
		defer m.recoverPanic(where)
		fn()
	}()
}

// sessionTask runs a loop the session can't work without. A panic ends the
// session, so the next one starts from clean state.
func (m *Miner) sessionTask(where string, end context.CancelFunc, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			m.Recovered(where, r)
			m.crashed.Store(true)
			end()
		}
	}()
	fn()
}

// safeHandler wraps a PubSub handler so a panic in it is checked by
// Recovered. Handlers already run off the read loop, one topic at a time.
func (m *Miner) safeHandler(where string, fn func(json.RawMessage)) func(json.RawMessage) {
	return func(msg json.RawMessage) {
		defer m.recoverPanic(where)
		fn(msg)
	}
}

// keepRunning restarts loop after a panic until ctx ends.
func (m *Miner) keepRunning(ctx context.Context, where string, loop func(context.Context)) {
	for ctx.Err() == nil {
		func() {
			defer m.recoverPanic(where)
			loop(ctx)
		}()
		if twitch.Sleep(ctx, time.Second) != nil {
			return
		}
	}
}

// crashPause spaces out sessions after one crashed, so a panic that repeats
// can't spin.
const crashPause = 30 * time.Second

// --- sleep and hibernate ---

const (
	resumeTick = 15 * time.Second
	// resumeSlack is how much later than expected a tick may arrive before
	// the computer is assumed to have been asleep.
	resumeSlack = time.Minute
)

// sleptFor reports how long the computer seems to have been suspended
// between two ticks, or 0. Wall-clock times are compared because the
// monotonic clock stops during sleep on some systems.
func sleptFor(last, now time.Time) time.Duration {
	gap := now.Round(0).Sub(last.Round(0))
	if gap > resumeTick+resumeSlack {
		return gap
	}
	return 0
}

// watchForResume notices the computer waking from sleep. Timers may then be
// late and Twitch connections are likely dead, so it catches up at once
// instead of waiting for them.
func (m *Miner) watchForResume(ctx context.Context) {
	t := time.NewTicker(resumeTick)
	defer t.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if gap := sleptFor(last, now); gap > 0 {
				m.resumed(gap)
			}
			last = now
		}
	}
}

func (m *Miner) resumed(gap time.Duration) {
	m.print(fmt.Sprintf("Woke up after about %s asleep; reconnecting and checking for campaigns", roughDuration(gap)))
	m.mu.Lock()
	pool := m.pool
	m.restartWatchingLocked()
	m.mu.Unlock()
	if pool != nil {
		pool.Reconnect()
	}
	m.RequestInventoryRefresh()
}
