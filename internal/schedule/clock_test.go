package schedule

import (
	"testing"
	"time"
)

func TestTestClockAdvances(t *testing.T) {
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	c := NewTestClock(start)

	if got := c.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %v, want %v", got, start)
	}

	c.Advance(90 * time.Minute)

	want := start.Add(90 * time.Minute)
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", got, want)
	}
}

// A TestClock must be safe to advance from one goroutine while the alarm loop
// reads it from another; every test in this package relies on that.
func TestTestClockIsRaceSafe(t *testing.T) {
	c := NewTestClock(time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC))
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			c.Advance(time.Second)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = c.Now()
	}
	<-done
}

// Advance is ordinary time passing: both the wall clock and the monotonic
// counter move forward by the same amount, so they never diverge.
func TestTestClockAdvanceMovesWallAndMono(t *testing.T) {
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	c := NewTestClock(start)

	wallBefore, monoBefore := c.Now(), c.Mono()
	c.Advance(90 * time.Minute)

	if got, want := c.Now(), wallBefore.Add(90*time.Minute); !got.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", got, want)
	}
	if got, want := c.Mono(), monoBefore+90*time.Minute; got != want {
		t.Fatalf("after Advance, Mono() = %v, want %v", got, want)
	}
}

// Suspend is what a sleeping laptop does: wall time jumps forward but the
// monotonic counter does not move at all. This is the only TestClock method
// that can reproduce, in a unit test, the wall/monotonic divergence that a
// real suspend produces -- which is the entire bug this application exists
// to survive.
func TestTestClockSuspendMovesWallNotMono(t *testing.T) {
	start := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	c := NewTestClock(start)

	monoBefore := c.Mono()
	c.Suspend(12*time.Hour + 30*time.Minute)

	if got, want := c.Now(), start.Add(12*time.Hour+30*time.Minute); !got.Equal(want) {
		t.Fatalf("after Suspend, Now() = %v, want %v", got, want)
	}
	if got := c.Mono(); got != monoBefore {
		t.Fatalf("after Suspend, Mono() = %v, want unchanged %v", got, monoBefore)
	}
}

func TestManualTickerDeliversTicks(t *testing.T) {
	mt := NewManualTicker()
	defer mt.Stop()

	go mt.Tick()

	select {
	case <-mt.C():
	case <-time.After(time.Second):
		t.Fatal("no tick delivered within 1s")
	}
}

// After Stop, a Tick() call must return promptly and must not panic sending
// on a closed channel. This would panic (or hang) against an implementation
// where Stop does close(m.ch).
func TestManualTickerTickAfterStopDoesNotPanicOrBlock(t *testing.T) {
	mt := NewManualTicker()
	mt.Stop()

	done := make(chan struct{})
	go func() {
		mt.Tick()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Tick() after Stop() did not return within 1s")
	}
}

// Stop must be idempotent: calling it twice must not panic (a second
// close of the same channel would panic).
func TestManualTickerStopTwiceDoesNotPanic(t *testing.T) {
	mt := NewManualTicker()
	mt.Stop()
	mt.Stop()
}

// After Stop, receiving on C() must behave like a real time.Ticker's channel
// after Stop: it blocks forever, it does not yield a spurious ready receive.
// If Stop closed the tick channel, this select would return the zero Time
// immediately instead of hitting the timeout branch.
func TestManualTickerCAfterStopDoesNotYieldSpuriousTick(t *testing.T) {
	mt := NewManualTicker()
	mt.Stop()

	select {
	case <-mt.C():
		t.Fatal("C() delivered a spurious tick after Stop()")
	case <-time.After(50 * time.Millisecond):
		// Expected: no tick, channel is not closed-and-always-ready.
	}
}
