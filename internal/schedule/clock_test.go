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
