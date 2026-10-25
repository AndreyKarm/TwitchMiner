package miner

import (
	"context"
	"testing"
	"time"

	"github.com/AndreyKarm/TwitchMiner/internal/config"
)

// panicTestMiner is a miner whose Recovered can run without a dashboard and
// that records exit calls instead of ending the test binary.
func panicTestMiner(t *testing.T) (*Miner, *int) {
	t.Helper()
	m := testMiner(config.Defaults())
	m.ui = nopUI{}
	exits := 0
	oldExit, oldWait := exitProcess, lockLeakWait
	exitProcess = func(int) { exits++ }
	lockLeakWait = 100 * time.Millisecond
	t.Cleanup(func() { exitProcess, lockLeakWait = oldExit, oldWait })
	return m, &exits
}

func TestGoSafeRecoversPanic(t *testing.T) {
	m, exits := panicTestMiner(t)
	done := make(chan struct{})
	m.goSafe("test", func() {
		defer close(done)
		panic("boom")
	})
	<-done
	// Recovered runs after fn's own defers; give it a moment to finish.
	time.Sleep(50 * time.Millisecond)
	if *exits != 0 {
		t.Fatal("a panic that left nothing locked must not exit")
	}
}

func TestPanicHoldingTheLockExits(t *testing.T) {
	m, exits := panicTestMiner(t)
	func() {
		defer m.recoverPanic("test")
		m.mu.Lock() // leaked: the panic skips the unlock
		panic("boom")
	}()
	if *exits != 1 {
		t.Fatal("a panic that leaked the state lock must exit instead of hanging")
	}
}

func TestSessionTaskEndsSessionOnPanic(t *testing.T) {
	m, _ := panicTestMiner(t)
	ctx, cancel := context.WithCancel(t.Context())
	m.sessionTask("test", cancel, func() { panic("boom") })
	if ctx.Err() == nil || !m.crashed.Load() {
		t.Fatal("a crashed session loop must end the session and mark it crashed")
	}
}

func TestKeepRunningRestartsAfterPanic(t *testing.T) {
	m, _ := panicTestMiner(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	runs := make(chan int, 3)
	n := 0
	go m.keepRunning(ctx, "test", func(context.Context) {
		n++
		runs <- n
		if n == 1 {
			panic("boom")
		}
		<-ctx.Done()
	})
	for want := 1; want <= 2; want++ {
		select {
		case got := <-runs:
			if got != want {
				t.Fatalf("run %d, want %d", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the loop was not restarted after a panic")
		}
	}
}

func TestSleptFor(t *testing.T) {
	last := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		gap  time.Duration
		want bool
	}{
		{resumeTick, false},
		{resumeTick + 30*time.Second, false}, // a busy system, not sleep
		{resumeTick + resumeSlack + time.Second, true},
		{2 * time.Hour, true},
	} {
		got := sleptFor(last, last.Add(c.gap)) > 0
		if got != c.want {
			t.Errorf("gap %v: slept = %v, want %v", c.gap, got, c.want)
		}
	}
}

func TestResumeCatchesUp(t *testing.T) {
	m := testMiner(config.Defaults())
	m.ui = nopUI{}
	m.stateCh, m.watchRestart = make(chan struct{}, 1), make(chan struct{}, 1)
	m.resumed(3 * time.Hour)
	if !m.inventoryPending {
		t.Fatal("waking up should queue an inventory reload")
	}
	select {
	case <-m.watchRestart:
	default:
		t.Fatal("waking up should restart the watch loop's wait")
	}
}
