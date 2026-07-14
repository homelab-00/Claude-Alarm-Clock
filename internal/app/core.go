// Package app orchestrates the alarm, the runner, and the store.
//
// It must never import Fyne. It emits Events; internal/ui is the only thing
// that renders them.
package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

// Status is what the app is doing.
type Status int

const (
	StatusIdle    Status = iota // disarmed, nothing to report
	StatusArmed                 // counting down
	StatusRunning               // claude is running right now
	StatusDone                  // claude answered
	StatusMissed                // the fire time passed unobserved, beyond grace
	StatusError                 // claude failed
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusArmed:
		return "armed"
	case StatusRunning:
		return "running"
	case StatusDone:
		return "done"
	case StatusMissed:
		return "missed"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// Event is one thing the UI needs to render.
type Event struct {
	Status Status
	Now    time.Time

	FireAt    time.Time
	Target    time.Time
	Remaining time.Duration // StatusArmed: until FireAt
	Late      time.Duration // StatusMissed: how far past FireAt we woke

	Result runner.Result // StatusDone
	Err    error         // StatusError
}

// Core wires the three seams together.
type Core struct {
	clk    schedule.Clock
	alarm  *schedule.Alarm
	runner runner.Runner
	store  config.Store

	out chan Event

	mu     sync.Mutex
	state  config.State
	status Status
}

// New returns a Core. Call Run in a goroutine, and run the Alarm too.
func New(clk schedule.Clock, al *schedule.Alarm, r runner.Runner, st config.Store) *Core {
	loaded, err := st.Load()
	if err != nil {
		loaded = config.DefaultState(".")
	}
	return &Core{
		clk:    clk,
		alarm:  al,
		runner: r,
		store:  st,
		out:    make(chan Event, 8),
		state:  loaded,
		status: StatusIdle,
	}
}

// Events is the stream the UI renders. Buffered, and Core never blocks on it:
// a slow UI drops ticks rather than stalling the alarm.
func (c *Core) Events() <-chan Event { return c.out }

// State returns the current persisted settings.
func (c *Core) State() config.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Arm schedules the alarm. This is an EXPLICIT user act.
//
// Per spec 5.3, the grace window is NOT consulted here. Grace answers "the app
// was not watching -- is this stale?", which is meaningless when the user is
// sitting in front of the app pressing the button. So if the computed fire time
// is already in the past but the target is still ahead (arming an 07:30 target
// with a 20-minute lead-in at 07:20), we fire immediately, however far past the
// fire time we are.
func (c *Core) Arm(st config.State) error {
	if err := st.Validate(); err != nil {
		return err
	}
	if err := st.ValidateWorkDir(); err != nil {
		return err
	}
	// Resolve claude now, at arm time, so a missing binary fails while the user
	// is looking at the app -- not at fire time, when nobody is.
	if _, err := runner.Lookup(); err != nil {
		return err
	}

	now := c.clk.Now()
	fire, target, err := st.Spec.FireAt(now)
	if err != nil {
		return err
	}

	st.Armed = true
	st.FireAt = fire
	st.Target = target

	c.mu.Lock()
	c.state = st
	c.mu.Unlock()

	if err := c.store.Save(st); err != nil {
		return fmt.Errorf("could not save settings: %w", err)
	}

	// The fire time is already gone, but the target is not. Fire now, ignoring
	// grace -- see the doc comment.
	if !now.Round(0).Before(fire.Round(0)) {
		c.alarm.Disarm()
		go c.fire(context.Background(), fire, target)
		return nil
	}

	c.alarm.Arm(fire, target, st.Spec.Grace)
	c.emit(Event{Status: StatusArmed, Now: now, FireAt: fire, Target: target, Remaining: fire.Sub(now)})
	return nil
}

// Restore re-arms a persisted alarm after a restart.
//
// Unlike Arm, this represents time the app was NOT watching, so the grace
// window DOES apply. An alarm that came due while the app was shut down and is
// now hours stale must be reported missed, not fired.
func (c *Core) Restore() error {
	st, err := c.store.Load()
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.state = st
	c.mu.Unlock()

	if !st.Armed || st.FireAt.IsZero() {
		c.setStatus(StatusIdle)
		c.emit(Event{Status: StatusIdle, Now: c.clk.Now()})
		return nil
	}

	now := c.clk.Now()

	switch schedule.Decide(now, st.FireAt, st.Spec.Grace) {
	case schedule.DecideMissed:
		c.disarmAndPersist()
		c.setStatus(StatusMissed)
		c.emit(Event{
			Status: StatusMissed, Now: now,
			FireAt: st.FireAt, Target: st.Target,
			Late: now.Round(0).Sub(st.FireAt.Round(0)),
		})

	case schedule.DecideFire:
		c.disarmAndPersist()
		go c.fire(context.Background(), st.FireAt, st.Target)

	case schedule.DecideWait:
		c.alarm.Arm(st.FireAt, st.Target, st.Spec.Grace)
		c.setStatus(StatusArmed)
		c.emit(Event{
			Status: StatusArmed, Now: now,
			FireAt: st.FireAt, Target: st.Target,
			Remaining: st.FireAt.Sub(now),
		})
	}
	return nil
}

// Disarm cancels a pending alarm.
func (c *Core) Disarm() {
	c.alarm.Disarm()
	c.disarmAndPersist()
	c.setStatus(StatusIdle)
	c.emit(Event{Status: StatusIdle, Now: c.clk.Now()})
}

// RunNow invokes claude immediately, ignoring the schedule entirely. This backs
// the "Run now" button offered in the MISSED state.
func (c *Core) RunNow() {
	go c.fire(context.Background(), time.Time{}, time.Time{})
}

// Run consumes the alarm's updates. It blocks until ctx is cancelled.
func (c *Core) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case u := <-c.alarm.Updates():
			switch u.Kind {
			case schedule.EventJump:
				// The wall clock moved behind our back. Every derived fire time
				// is now suspect -- recompute it from the Spec, which is the
				// only thing that survives a suspend, an NTP step, a DST change,
				// or a timezone change.
				c.recomputeAfterJump(u.Now)

			case schedule.EventTick:
				if armed, _, _ := c.alarm.Armed(); armed {
					c.emit(Event{
						Status: StatusArmed, Now: u.Now,
						FireAt: u.FireAt, Target: u.Target, Remaining: u.Remaining,
					})
				} else if c.currentStatus() == StatusIdle {
					c.emit(Event{Status: StatusIdle, Now: u.Now})
				}

			case schedule.EventFire:
				c.disarmAndPersist()
				go c.fire(ctx, u.FireAt, u.Target)

			case schedule.EventMissed:
				c.disarmAndPersist()
				c.setStatus(StatusMissed)
				c.emit(Event{
					Status: StatusMissed, Now: u.Now,
					FireAt: u.FireAt, Target: u.Target, Late: u.Late,
				})
			}
		}
	}
}

// fire runs claude and reports the outcome.
func (c *Core) fire(ctx context.Context, fireAt, target time.Time) {
	st := c.State()

	bin, err := runner.Lookup()
	if err != nil {
		c.setStatus(StatusError)
		c.emit(Event{Status: StatusError, Now: c.clk.Now(), Err: err})
		return
	}

	c.setStatus(StatusRunning)
	c.emit(Event{Status: StatusRunning, Now: c.clk.Now(), FireAt: fireAt, Target: target})

	res, err := c.runner.Run(ctx, runner.Config{
		Bin:       bin,
		WorkDir:   st.WorkDir,
		Model:     st.Model,
		Prompt:    st.Prompt,
		BudgetUSD: runner.DefaultBudgetUSD,
		Timeout:   runner.DefaultTimeout,
	})
	if err != nil {
		c.setStatus(StatusError)
		c.emit(Event{Status: StatusError, Now: c.clk.Now(), FireAt: fireAt, Target: target, Err: err})
		return
	}

	c.setStatus(StatusDone)
	c.emit(Event{Status: StatusDone, Now: c.clk.Now(), FireAt: fireAt, Target: target, Result: res})
}

// recomputeAfterJump re-derives the fire time from the Spec after the wall clock
// moved. The Spec is the only durable truth; an absolute instant is not.
func (c *Core) recomputeAfterJump(now time.Time) {
	armed, _, _ := c.alarm.Armed()
	if !armed {
		return
	}

	st := c.State()
	fire, target, err := st.Spec.FireAt(now)
	if err != nil {
		return
	}

	st.FireAt, st.Target = fire, target
	c.mu.Lock()
	c.state = st
	c.mu.Unlock()
	_ = c.store.Save(st)

	c.alarm.Arm(fire, target, st.Spec.Grace)
}

func (c *Core) disarmAndPersist() {
	c.mu.Lock()
	c.state.Armed = false
	c.state.FireAt = time.Time{}
	c.state.Target = time.Time{}
	st := c.state
	c.mu.Unlock()
	_ = c.store.Save(st)
}

func (c *Core) setStatus(s Status) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
}

func (c *Core) currentStatus() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// emit never blocks. A slow UI drops a tick; it must never stall the alarm.
func (c *Core) emit(e Event) {
	select {
	case c.out <- e:
	default:
	}
}
