package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

type rig struct {
	t     *testing.T
	clk   *schedule.TestClock
	tick  *schedule.ManualTicker
	fake  *runner.Fake
	store *config.MemStore
	core  *Core
}

func newRig(t *testing.T, start time.Time) *rig {
	t.Helper()

	clk := schedule.NewTestClock(start)
	mt := schedule.NewManualTicker()
	al := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt })
	fake := &runner.Fake{Result: runner.Result{Text: "Hello! How can I help you today?", CostUSD: 0.0044}}
	store := config.NewMemStore(config.DefaultState(t.TempDir()))

	c := New(clk, al, fake, store)

	ctx, cancel := context.WithCancel(context.Background())
	go al.Run(ctx)
	go c.Run(ctx)
	t.Cleanup(cancel)

	return &rig{t: t, clk: clk, tick: mt, fake: fake, store: store, core: c}
}

// step advances the clock, ticks once, and drains the events that follow.
func (r *rig) step(d time.Duration) []Event {
	r.t.Helper()
	r.clk.Advance(d)
	r.tick.Tick()

	var got []Event
	for {
		select {
		case e := <-r.core.Events():
			got = append(got, e)
		case <-time.After(300 * time.Millisecond):
			return got
		}
	}
}

// drain discards any events already buffered on the Core's channel, so a
// later step's collected events reflect only what happens from that point
// on.
func (r *rig) drain() {
	for {
		select {
		case <-r.core.Events():
		default:
			return
		}
	}
}

func (r *rig) armed(spec schedule.Spec) config.State {
	r.t.Helper()
	st := r.store.MustLoad()
	st.Spec = spec
	if err := r.core.Arm(st); err != nil {
		r.t.Fatalf("Arm() error = %v", err)
	}
	return st
}

func lastStatus(es []Event) (Status, bool) {
	if len(es) == 0 {
		return 0, false
	}
	return es[len(es)-1].Status, true
}

func hasStatus(es []Event, s Status) bool {
	for _, e := range es {
		if e.Status == s {
			return true
		}
	}
	return false
}

func TestCoreArmThenFireRunsClaudeOnce(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	// Target 07:30, offset 20m -> fire at 07:10, ten minutes away.
	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	if got := r.step(time.Second); !hasStatus(got, StatusArmed) {
		t.Fatalf("want StatusArmed while counting down, got %v", got)
	}
	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran before the alarm was due")
	}

	got := r.step(10 * time.Minute)

	if !hasStatus(got, StatusDone) {
		t.Fatalf("want StatusDone after firing, got %v", got)
	}
	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want exactly 1", r.fake.CallCount())
	}

	call, _ := r.fake.LastCall()
	if call.Prompt != "Hello world" {
		t.Fatalf("prompt = %q, want %q", call.Prompt, "Hello world")
	}
	if call.Model != "haiku" {
		t.Fatalf("model = %q, want haiku", call.Model)
	}
	if call.WorkDir == "" {
		t.Fatal("WorkDir was not passed to the runner")
	}

	// The result reaches the UI.
	for _, e := range got {
		if e.Status == StatusDone && e.Result.Text != "Hello! How can I help you today?" {
			t.Fatalf("Result.Text = %q", e.Result.Text)
		}
	}
}

// After firing, the core must be idle-ish and disarmed -- never silently
// rescheduled. The spec is explicit: one-shot.
func TestCoreDoesNotRearmItselfAfterFiring(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})
	r.step(10 * time.Minute) // fires

	// A full day later it must not have run again.
	r.step(24 * time.Hour)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times over 24h; a one-shot alarm must run once", r.fake.CallCount())
	}
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("the persisted state is still armed after firing")
	}
}

// A 12h30m suspend past the grace window: MISSED, and claude must NOT run.
func TestCoreSuspendPastGraceIsMissedAndDoesNotRunClaude(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	got := r.step(12*time.Hour + 30*time.Minute)

	if !hasStatus(got, StatusMissed) {
		t.Fatalf("want StatusMissed after a 12h30m suspend, got %v", got)
	}
	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran after a 12h30m suspend; a stale alarm must not fire")
	}
}

// THE distinction from spec 5.3. Arming is an explicit act with the user
// watching, so a fire time already in the past fires IMMEDIATELY -- the grace
// window is not consulted. Grace governs unobserved time only.
//
// Here: the user arms a 07:30 target with a 20-minute lead-in at 07:20. The
// fire time (07:10) is already ten minutes gone -- twice the 5-minute grace --
// but the intent ("do this before 07:30") is still satisfiable, so we run.
func TestCoreArmWithFireTimeAlreadyPastFiresImmediatelyIgnoringGrace(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 20, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	// Give the immediate run a moment to land.
	got := r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want 1: arming with a past fire time must fire immediately", r.fake.CallCount())
	}
	if !hasStatus(got, StatusDone) && !hasStatus(got, StatusRunning) {
		t.Fatalf("want Running or Done, got %v", got)
	}
}

// Restore is the opposite: it represents time the app was NOT watching (it was
// shut down), so the grace window DOES apply.
func TestCoreRestoreHonoursTheGraceWindow(t *testing.T) {
	// The app was armed to fire at 07:10 and then shut down. It restarts at
	// 09:00, nearly two hours late.
	start := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	r := newRig(t, start)

	st := config.DefaultState(t.TempDir())
	st.Spec = schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute}
	st.Armed = true
	st.FireAt = time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local)
	st.Target = time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local)
	if err := r.store.Save(st); err != nil {
		t.Fatal(err)
	}

	if err := r.core.Restore(); err != nil {
		t.Fatal(err)
	}

	got := r.step(time.Second)

	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran on restore of an alarm 1h50m stale; grace must apply to unobserved time")
	}
	if !hasStatus(got, StatusMissed) {
		t.Fatalf("want StatusMissed, got %v", got)
	}
}

// A restore inside the grace window does fire.
func TestCoreRestoreWithinGraceFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 13, 0, 0, time.Local) // 3m after fire
	r := newRig(t, start)

	st := config.DefaultState(t.TempDir())
	st.Spec = schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute}
	st.Armed = true
	st.FireAt = time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local)
	st.Target = time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local)
	if err := r.store.Save(st); err != nil {
		t.Fatal(err)
	}

	if err := r.core.Restore(); err != nil {
		t.Fatal(err)
	}
	r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want 1: 3m late is inside a 5m grace", r.fake.CallCount())
	}
}

// A restore of a state that was never armed does nothing.
func TestCoreRestoreOfDisarmedStateDoesNothing(t *testing.T) {
	start := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	r := newRig(t, start)

	if err := r.core.Restore(); err != nil {
		t.Fatal(err)
	}
	got := r.step(time.Second)

	if r.fake.CallCount() != 0 {
		t.Fatal("claude ran on restore of a disarmed state")
	}
	if s, ok := lastStatus(got); ok && s != StatusIdle {
		t.Fatalf("status = %v, want StatusIdle", s)
	}
}

func TestCoreRunnerFailureBecomesStatusError(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)
	r.fake.Err = errors.New("claude timed out after 2m0s")

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	got := r.step(10 * time.Minute)

	if !hasStatus(got, StatusError) {
		t.Fatalf("want StatusError when the runner fails, got %v", got)
	}
	for _, e := range got {
		if e.Status == StatusError && e.Err == nil {
			t.Fatal("StatusError event carries no error")
		}
	}
}

func TestCoreDisarmStopsIt(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})
	r.core.Disarm()

	r.step(30 * time.Minute)

	if r.fake.CallCount() != 0 {
		t.Fatal("a disarmed alarm fired")
	}
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("Disarm did not persist")
	}
}

func TestCoreRunNowInvokesClaudeImmediately(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.core.RunNow()
	r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("RunNow ran claude %d times, want 1", r.fake.CallCount())
	}
}

func TestCoreArmRejectsAnInvalidState(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	bad := config.DefaultState(t.TempDir())
	bad.Spec.Hour = 99

	if err := r.core.Arm(bad); err == nil {
		t.Fatal("Arm accepted an invalid state")
	}
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("a rejected Arm must not persist as armed")
	}
}

// A time jump must cause the pending fire time to be recomputed from the
// Spec, not acted on as a stale absolute instant.
//
// The jump crosses a day boundary: armed for 23:00 on 07-14, then a 20h
// suspend carries the wall clock to 03:00 on 07-15. The STALE fire time
// (23:00 07-14) is now ~4h in the past -- far beyond the 5-minute grace --
// so an alarm that acts on it without recomputing would wrongly conclude
// MISSED. The correct behaviour is to re-derive the fire time from the Spec,
// landing on 23:00 of the NEW day, and stay armed and quiet.
//
// This is deliberately NOT the same shape as a same-day jump (e.g. 2h
// backwards within the same day): that leaves the "next occurrence of
// 23:00" unchanged, so a no-op recompute reaches the same conclusion as a
// working one and the test cannot discriminate between them. Crossing
// midnight changes which occurrence is next, so only a genuine recompute
// lands on the right answer.
func TestCoreRecomputesFireTimeAfterATimeJump(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 23, Minute: 0, Offset: 0, Grace: 5 * time.Minute})
	r.drain() // discard the StatusArmed event from arming; isolate the jump's effect

	// The laptop lid closes. Wall clock jumps forward 20h -- past midnight --
	// while the monotonic clock does not move: a real suspend, per
	// TestClock.Suspend's contract.
	r.clk.Suspend(20 * time.Hour)
	got := r.step(0)

	if r.fake.CallCount() != 0 {
		t.Fatalf("claude ran %d times; a stale-but-recomputed alarm must not fire", r.fake.CallCount())
	}
	if hasStatus(got, StatusMissed) {
		t.Fatalf("reported MISSED off the stale pre-jump fire time instead of recomputing: %v", got)
	}

	wantFireAt := time.Date(2026, 7, 15, 23, 0, 0, 0, time.Local)

	armed, fireAt, _ := r.core.alarm.Armed()
	if !armed {
		t.Fatal("live alarm disarmed after the jump; the recompute must keep it pending")
	}
	if !fireAt.Equal(wantFireAt) {
		t.Fatalf("live FireAt = %v, want %v (23:00 on the NEW day)", fireAt, wantFireAt)
	}

	// This is the assertion that catches the state divergence: the live
	// alarm and the persisted state must agree. If Core's EventMissed
	// handling raced ahead of (or behind) the recompute's own Arm(), the
	// live alarm can end up armed while the persisted copy is left
	// Armed=false -- silently losing the alarm on the next restart.
	st := r.store.MustLoad()
	if !st.Armed {
		t.Fatal("persisted state Armed = false after the jump; STATE DIVERGED from the live alarm")
	}
	if !st.FireAt.Equal(wantFireAt) {
		t.Fatalf("persisted FireAt = %v, want %v (23:00 on the NEW day)", st.FireAt, wantFireAt)
	}
}

// A full Events channel must not silently drop a terminal status the way it
// drops a tick. StatusDone carries the only copy of the runner's Result --
// config.State has no field for one -- so losing it to a full buffer is
// unrecoverable; the user would never see the answer their alarm ran for.
//
// This fills the buffered channel to capacity directly (same package: out is
// unexported), fires the alarm, and confirms StatusDone still arrives once
// draining resumes. A non-blocking (dropping) implementation would have
// discarded it the instant it landed on the already-full buffer, and this
// test would time out.
func TestCoreTerminalEventIsNotDroppedWhenTheChannelIsFull(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})
	r.drain() // discard the StatusArmed event from arming

	// Fill the buffered channel to capacity without anybody draining it.
	for i := 0; i < cap(r.core.out); i++ {
		r.core.out <- Event{Status: StatusArmed}
	}

	// Fire the alarm. StatusRunning may legitimately drop (it is not
	// terminal), but StatusDone must block for room rather than vanish.
	r.clk.Advance(10 * time.Minute)
	r.tick.Tick()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-r.core.Events():
			if e.Status == StatusDone {
				if r.fake.CallCount() != 1 {
					t.Fatalf("claude ran %d times, want 1", r.fake.CallCount())
				}
				return
			}
		case <-deadline:
			t.Fatal("StatusDone never arrived after the channel had room; it was dropped")
		}
	}
}
