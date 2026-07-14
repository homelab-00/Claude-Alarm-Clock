package schedule

import (
	"context"
	"sync"
	"time"
)

// EventKind is the type of an Update.
type EventKind int

const (
	// EventTick is emitted on every tick, armed or not. It drives the UI clock.
	EventTick EventKind = iota
	// EventFire means: run the job now.
	EventFire
	// EventMissed means the fire time passed while nobody was watching, by more
	// than the grace window. The job is NOT run.
	EventMissed
	// EventJump means the wall clock moved independently of the monotonic clock:
	// the machine suspended, or the clock was stepped. Every derived fire time
	// must be recomputed from the Spec.
	EventJump
)

func (k EventKind) String() string {
	switch k {
	case EventTick:
		return "tick"
	case EventFire:
		return "fire"
	case EventMissed:
		return "missed"
	case EventJump:
		return "jump"
	default:
		return "unknown"
	}
}

// Update is one thing the alarm loop noticed.
type Update struct {
	Kind EventKind
	Now  time.Time // wall clock, monotonic reading already stripped

	FireAt time.Time // zero if disarmed
	Target time.Time // zero if disarmed

	Remaining time.Duration // EventTick, when armed: how long until FireAt
	Late      time.Duration // EventMissed: how far past FireAt we woke up
	Jump      time.Duration // EventJump: how far the wall clock moved
}

// Alarm is a single, always-running poll loop.
//
// It ticks for the entire life of the app, whether armed or not, so the UI has
// a clock. Arm and Disarm mutate state that the loop reads on its next tick.
//
// Because there is only ever ONE loop, there is no spawn, no cancel, and no
// respawn -- and therefore no double-fire race to guard against with a
// generation counter. Re-arming simply overwrites the pending fire time.
type Alarm struct {
	clk       Clock
	period    time.Duration
	newTicker TickerFunc
	out       chan Update

	// initialWall is the wall clock reading (monotonic stripped, if any) at
	// construction time. Run reads it exactly once, as its loop-local starting
	// baseline, and never again -- from then on it is purely local state inside
	// Run's single goroutine, so it needs no mutex.
	//
	// It must be captured here, in NewAlarm, rather than lazily as the first
	// statement inside Run: Run executes in its own goroutine, started with
	// `go a.Run(ctx)`, and the caller is free to mutate the Clock (e.g. a
	// test's TestClock.Advance) immediately after that statement, before the
	// goroutine has necessarily been scheduled. Reading a.clk.Now() lazily
	// inside Run would race that mutation -- and losing the race silently
	// produces a zero baseline delta, hiding exactly the suspend this package
	// exists to detect. Capturing it here instead relies on the Go memory
	// model's guarantee that a goroutine's `go` statement happens-before the
	// spawned goroutine's execution: everything NewAlarm did, including this
	// read, is visible to Run without further synchronization.
	initialWall time.Time

	mu     sync.Mutex
	armed  bool
	fireAt time.Time
	target time.Time
	grace  time.Duration
}

// NewAlarm returns an Alarm. Call Run in a goroutine to start it.
func NewAlarm(clk Clock, period time.Duration, newTicker TickerFunc) *Alarm {
	return &Alarm{
		clk:         clk,
		period:      period,
		newTicker:   newTicker,
		out:         make(chan Update),
		initialWall: clk.Now().Round(0),
	}
}

// Updates is the stream of everything the loop notices. It is unbuffered: the
// consumer must keep reading, or the loop blocks.
func (a *Alarm) Updates() <-chan Update { return a.out }

// Arm schedules a fire at fireAt. It replaces any pending alarm.
func (a *Alarm) Arm(fireAt, target time.Time, grace time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.armed = true
	a.fireAt = fireAt.Round(0)
	a.target = target.Round(0)
	a.grace = grace
}

// Disarm cancels any pending alarm. The loop keeps ticking.
func (a *Alarm) Disarm() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.armed = false
	a.fireAt = time.Time{}
	a.target = time.Time{}
}

// Armed reports the current armed state and the pending times.
func (a *Alarm) Armed() (bool, time.Time, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.armed, a.fireAt, a.target
}

// Run is the loop. It blocks until ctx is cancelled.
func (a *Alarm) Run(ctx context.Context) {
	tk := a.newTicker(a.period)
	defer tk.Stop()

	// lastWall is the wall clock (monotonic reading stripped, if any) as of the
	// previous tick. We compare its delta against the ticker's own nominal
	// period rather than against a second, independently-read monotonic clock:
	// Clock is the seam tests use to fake wall time (TestClock.Advance jumps it
	// instantly), and that fake has no real monotonic reading to diverge from
	// -- a value built by time.Date, as every TestClock reading is, never
	// carries one, so Round(0) is a no-op on it and a second a.clk.Now() call
	// would agree with the first by construction. What IS real and unfakeable
	// in both production and tests is the ticker's cadence: under normal
	// operation a tick means "one period has elapsed", suspend or not. So
	// "period" stands in for the expected monotonic delta, and DetectJump
	// catches the case where the wall clock moved by far more (or less, or
	// backwards) than that -- exactly the "wall advances, monotonic does not"
	// suspend signature TestDetectJump documents.
	lastWall := a.initialWall

	for {
		select {
		case <-ctx.Done():
			return

		case <-tk.C():
			now := a.clk.Now().Round(0)

			if jump, jumped := DetectJump(now.Sub(lastWall), a.period, a.period); jumped {
				if !a.emit(ctx, Update{Kind: EventJump, Now: now, Jump: jump}) {
					return
				}
			}
			lastWall = now

			armed, fireAt, target, grace := a.snapshot()
			if !armed {
				if !a.emit(ctx, Update{Kind: EventTick, Now: now}) {
					return
				}
				continue
			}

			switch Decide(now, fireAt, grace) {
			case DecideWait:
				if !a.emit(ctx, Update{
					Kind: EventTick, Now: now,
					FireAt: fireAt, Target: target,
					Remaining: fireAt.Sub(now),
				}) {
					return
				}

			case DecideFire:
				a.Disarm() // before emitting, so we cannot fire twice
				if !a.emit(ctx, Update{
					Kind: EventFire, Now: now,
					FireAt: fireAt, Target: target,
				}) {
					return
				}

			case DecideMissed:
				a.Disarm()
				if !a.emit(ctx, Update{
					Kind: EventMissed, Now: now,
					FireAt: fireAt, Target: target,
					Late: now.Sub(fireAt),
				}) {
					return
				}
			}
		}
	}
}

func (a *Alarm) snapshot() (bool, time.Time, time.Time, time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.armed, a.fireAt, a.target, a.grace
}

// emit sends u, or reports false if ctx was cancelled while we waited.
func (a *Alarm) emit(ctx context.Context, u Update) bool {
	select {
	case a.out <- u:
		return true
	case <-ctx.Done():
		return false
	}
}
