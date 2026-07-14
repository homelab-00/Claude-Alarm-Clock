// Package schedule owns all alarm timing. It must never import Fyne.
package schedule

import (
	"sync"
	"time"
)

// Clock is the seam that lets tests simulate a 12-hour suspend in microseconds.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// NewRealClock returns a Clock backed by time.Now.
func NewRealClock() Clock { return realClock{} }

// TestClock is a Clock whose time only moves when a test moves it.
type TestClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewTestClock returns a TestClock reading the given instant.
func NewTestClock(t time.Time) *TestClock { return &TestClock{now: t} }

func (c *TestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d. Use it to simulate suspend.
func (c *TestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set jumps the clock to t. Use it to simulate an NTP step or a TZ change.
func (c *TestClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Ticker is the seam that lets tests drive the alarm loop one tick at a time.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// TickerFunc constructs a Ticker with the given period.
type TickerFunc func(time.Duration) Ticker

type realTicker struct{ t *time.Ticker }

func (r realTicker) C() <-chan time.Time { return r.t.C }
func (r realTicker) Stop()               { r.t.Stop() }

// RealTicker is the production TickerFunc.
//
// Note this is the one legitimate use of a monotonic timer in this package: it
// only paces the *polling*, it never decides when the alarm fires. The fire
// decision always re-reads the wall clock. A late tick makes the alarm late by
// at most one period; it can never make it fire at the wrong time.
func RealTicker(d time.Duration) Ticker { return realTicker{t: time.NewTicker(d)} }

// ManualTicker is a Ticker that only ticks when a test says so.
type ManualTicker struct {
	ch   chan time.Time
	once sync.Once
}

// NewManualTicker returns a ManualTicker with an unbuffered channel.
func NewManualTicker() *ManualTicker {
	return &ManualTicker{ch: make(chan time.Time)}
}

// Tick delivers one tick, blocking until the loop receives it. The value sent
// is deliberately the zero Time: the alarm loop must read the Clock, never the
// tick payload. A test that depends on the payload is testing the wrong thing.
func (m *ManualTicker) Tick() { m.ch <- time.Time{} }

func (m *ManualTicker) C() <-chan time.Time { return m.ch }

func (m *ManualTicker) Stop() { m.once.Do(func() { close(m.ch) }) }
