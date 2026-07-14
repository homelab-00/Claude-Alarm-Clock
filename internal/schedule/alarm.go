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

	// initialWall and initialMono are the wall and monotonic readings (wall's
	// monotonic component stripped, if any) at construction time. Run reads
	// them exactly once, as its loop-local starting baseline, and never again
	// -- from then on they are purely local state inside Run's single
	// goroutine, so they need no mutex.
	//
	// They must be captured here, in NewAlarm, rather than lazily as the first
	// statement inside Run: Run executes in its own goroutine, started with
	// `go a.Run(ctx)`, and the caller is free to mutate the Clock (e.g. a
	// test's TestClock.Advance or TestClock.Suspend) immediately after that
	// statement, before the goroutine has necessarily been scheduled. Reading
	// a.clk.Now()/Mono() lazily inside Run would race that mutation -- and
	// losing the race silently produces a zero baseline delta, hiding exactly
	// the suspend this package exists to detect. Capturing them here instead
	// relies on the Go memory model's guarantee that a goroutine's `go`
	// statement happens-before the spawned goroutine's execution: everything
	// NewAlarm did, including these reads, is visible to Run without further
	// synchronization.
	initialWall time.Time
	initialMono time.Duration

	mu     sync.Mutex
	armed  bool
	fireAt time.Time
	target time.Time
	grace  time.Duration

	// beforeDisarm, if set, is called by Run immediately after it decides to
	// fire or report an alarm missed, but before it attempts to clear the
	// pending alarm. Production code never sets it, so it costs one nil
	// check per fire/missed tick.
	//
	// It exists solely so a test can force the TOCTOU gap that
	// disarmIfStill closes: Run reads state under the lock (snapshot),
	// releases it, calls Decide, and only THEN re-acquires the lock to
	// clear the alarm. A concurrent Arm from the UI goroutine can land in
	// that gap. There is no channel operation in that gap to block a test
	// on -- Decide is pure and returns synchronously -- so without this
	// hook the interleaving can only be hit by chance under scheduler
	// preemption, which is exactly the non-deterministic test this project
	// has already rejected twice. Setting beforeDisarm to a function that
	// blocks lets a test pause the loop here on purpose. See
	// TestAlarmRearmDuringFireIsNotClobbered in alarm_test.go.
	//
	// Like initialWall/initialMono above, it needs no mutex: a test sets it
	// before calling `go a.Run(ctx)`, and only Run's own goroutine ever
	// reads or clears it afterward, so the go-statement happens-before
	// guarantee alone makes this safe.
	beforeDisarm func()
}

// NewAlarm returns an Alarm. Call Run in a goroutine to start it.
func NewAlarm(clk Clock, period time.Duration, newTicker TickerFunc) *Alarm {
	return &Alarm{
		clk:         clk,
		period:      period,
		newTicker:   newTicker,
		out:         make(chan Update),
		initialWall: clk.Now().Round(0),
		initialMono: clk.Mono(),
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

	// lastWall and lastMono are the wall and monotonic readings as of the
	// previous tick. A suspend is detected by comparing how far each moved
	// between ticks: under normal operation, including a merely late or
	// starved tick, wall and monotonic advance together. A suspend is the one
	// thing that moves wall time forward while CLOCK_MONOTONIC stands still
	// (see realClock.Mono in clock.go), so DetectJump flags a divergence
	// between the two deltas, not a deviation of either one from the nominal
	// period. TestClock.Suspend is what reproduces that divergence in tests;
	// TestClock.Advance moves both, precisely so it does NOT trip this check.
	lastWall := a.initialWall
	lastMono := a.initialMono

	for {
		select {
		case <-ctx.Done():
			return

		case <-tk.C():
			now := a.clk.Now().Round(0)
			mono := a.clk.Mono()

			if jump, jumped := DetectJump(now.Sub(lastWall), mono-lastMono, a.period); jumped {
				if !a.emit(ctx, Update{Kind: EventJump, Now: now, Jump: jump}) {
					return
				}
			}
			lastWall, lastMono = now, mono

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
				if a.beforeDisarm != nil {
					a.beforeDisarm()
				}
				if !a.disarmIfStill(fireAt) {
					// A concurrent Arm landed between snapshot() and here:
					// the alarm we just decided to fire has already been
					// replaced by a new one. Firing now would run the job
					// for an alarm the user no longer has pending, and
					// clearing unconditionally would silently throw the new
					// one away. Do neither -- let the new alarm be judged
					// fresh on its own next tick.
					continue
				}
				if !a.emit(ctx, Update{
					Kind: EventFire, Now: now,
					FireAt: fireAt, Target: target,
				}) {
					return
				}

			case DecideMissed:
				if a.beforeDisarm != nil {
					a.beforeDisarm()
				}
				if !a.disarmIfStill(fireAt) {
					continue // superseded by a concurrent Arm; see DecideFire above
				}
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

// disarmIfStill clears the pending alarm only if fireAt is still the instant
// the caller decided to act on, and reports whether it did.
//
// Run reads state under the lock (snapshot), releases it to call the pure
// Decide, and only then comes back to clear the alarm. Between those two
// moments the UI goroutine may have called Arm with a NEW fire time -- and an
// unconditional clear would silently discard it: a lost update that no error
// ever surfaces. Comparing fireAt before clearing is what makes it safe for the
// loop to run alongside a user who re-arms: if the pending alarm no longer
// matches what was decided on, it has already been superseded, so this
// leaves it alone and reports false instead.
func (a *Alarm) disarmIfStill(fireAt time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.armed || !a.fireAt.Equal(fireAt) {
		return false // superseded by a concurrent Arm; leave it alone
	}
	a.armed = false
	a.fireAt = time.Time{}
	a.target = time.Time{}
	return true
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
