// Package schedule owns all alarm timing. It must never import Fyne.
package schedule

import (
	"sync"
	"time"
)

// Clock is the seam that lets tests simulate a 12-hour suspend in microseconds.
type Clock interface {
	Now() time.Time      // wall clock
	Mono() time.Duration // monotonic reading; only DIFFERENCES between calls are meaningful
}

// processStart is read once, at package init, via time.Now() -- so it carries
// a monotonic reading. It exists solely so realClock.Mono can hand back a
// Duration derived from a Sub of two monotonic-bearing Times.
var processStart = time.Now()

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Mono returns a duration that only advances while the machine is awake.
//
// time.Since(processStart) is time.Now().Sub(processStart). Both operands
// carry a monotonic reading (processStart because it too came from
// time.Now()), so Sub uses the monotonic clock per the time package's rules.
// On Linux that monotonic clock is CLOCK_MONOTONIC, which does not tick
// across a suspend -- unlike the wall clock, which jumps forward by however
// long the machine was asleep. That divergence between Now() and Mono() is
// exactly the suspend signature Alarm.Run watches for.
func (realClock) Mono() time.Duration { return time.Since(processStart) }

// NewRealClock returns a Clock backed by time.Now.
func NewRealClock() Clock { return realClock{} }

// TestClock is a Clock whose time only moves when a test moves it.
type TestClock struct {
	mu   sync.Mutex
	now  time.Time
	mono time.Duration
}

// NewTestClock returns a TestClock reading the given instant, with its
// monotonic counter starting at zero.
func NewTestClock(t time.Time) *TestClock { return &TestClock{now: t} }

func (c *TestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Mono returns the current monotonic reading. Only differences between two
// calls are meaningful, matching realClock.Mono's contract.
func (c *TestClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

// Advance moves the clock forward by d, wall and monotonic alike. Use it to
// simulate ordinary time passing -- including a slow or starved tick, where
// the process merely didn't get scheduled promptly. Both clocks still agree
// on how much time passed, so this must never be mistaken for a suspend.
func (c *TestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.mono += d
}

// Suspend moves the wall clock forward by d while leaving the monotonic
// counter untouched. This is precisely what a sleeping laptop does: wall
// time keeps passing (the RTC keeps ticking) while CLOCK_MONOTONIC does not.
// It is the only way to reproduce, in a test, the wall/monotonic divergence
// this entire application exists to detect and survive.
func (c *TestClock) Suspend(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Set jumps the wall clock to t, leaving the monotonic counter unaffected.
// Use it to simulate an NTP step, a manual clock change, or a TZ change --
// none of which touch the monotonic clock either.
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
//
// Stop must never close ch, the channel C() hands out. Closing it would make
// two things happen, both wrong: a Tick() racing with (or following) Stop()
// would panic sending on a closed channel, and a receive on C() after Stop()
// would return immediately and repeatedly with a zero Time instead of
// blocking forever like the real time.Ticker does. A loop written against
// the real ticker's mental model — select { case <-tk.C(): ... } — would
// busy-spin under that behavior, a bug that could only ever show up in
// tests. So Stop() closes a separate done channel instead, and ch is never
// closed.
type ManualTicker struct {
	ch   chan time.Time
	done chan struct{}
	once sync.Once
}

// NewManualTicker returns a ManualTicker with an unbuffered channel.
func NewManualTicker() *ManualTicker {
	return &ManualTicker{ch: make(chan time.Time), done: make(chan struct{})}
}

// Tick delivers one tick, blocking until the loop receives it or Stop is
// called, whichever comes first. The value sent is deliberately the zero
// Time: the alarm loop must read the Clock, never the tick payload. A test
// that depends on the payload is testing the wrong thing.
func (m *ManualTicker) Tick() {
	select {
	case m.ch <- time.Time{}:
	case <-m.done:
	}
}

// C returns the tick channel. It is never closed, so receiving on it after
// Stop blocks forever — matching time.Ticker's post-Stop behavior exactly.
func (m *ManualTicker) C() <-chan time.Time { return m.ch }

// Stop is idempotent and safe to call concurrently with Tick.
func (m *ManualTicker) Stop() { m.once.Do(func() { close(m.done) }) }
