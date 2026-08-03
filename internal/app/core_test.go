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

	// Capture the exact WorkDir the rig's store was seeded with -- arming
	// never touches WorkDir, so this is exactly what must reach the runner.
	wantDir := r.store.MustLoad().WorkDir
	if wantDir == "" {
		t.Fatal("test setup: rig's default state has an empty WorkDir")
	}

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
	// Not just non-empty: exactly the directory the user configured.
	// Mutating Core.fire to pass a different WorkDir (e.g. "/") would run
	// Claude in the wrong directory, and the old `!= ""` check let that
	// regression through silently.
	if call.WorkDir != wantDir {
		t.Fatalf("WorkDir = %q, want %q", call.WorkDir, wantDir)
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

	// Arm's immediate-fire branch must persist the disarm synchronously, just
	// like every other path that decides to fire (see
	// TestCoreDoesNotRearmItselfAfterFiring for the equivalent assertion on
	// the normal path). Arm() does this before returning, so it can be
	// checked immediately -- no need to wait for the fire itself to land.
	// Without it, Armed=true and a stale FireAt survive in the store, and a
	// restart within the grace window replays the alarm: see
	// TestCoreRestoreAfterArmWithPastFireTimeDoesNotFireAgain below.
	if st := r.store.MustLoad(); st.Armed {
		t.Fatal("Arm's immediate-fire path did not persist the disarm; the store still says Armed = true")
	}

	// Give the immediate run a moment to land.
	got := r.step(time.Second)

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times, want 1: arming with a past fire time must fire immediately", r.fake.CallCount())
	}
	if !hasStatus(got, StatusDone) && !hasStatus(got, StatusRunning) {
		t.Fatalf("want Running or Done, got %v", got)
	}
}

// The double-fire scenario from the final review: arm a target whose fire
// time has already passed (fires immediately, per the test above), quit, and
// reopen within the grace window. If the immediate-fire branch had not
// persisted its disarm, Restore would find Armed=true with a stale FireAt
// still just inside the grace window, decide DecideFire all over again, and
// run Claude a second time -- unrequested, and paid for twice.
func TestCoreRestoreAfterArmWithPastFireTimeDoesNotFireAgain(t *testing.T) {
	clk := schedule.NewTestClock(time.Date(2026, 7, 14, 7, 12, 0, 0, time.Local))
	store := config.NewMemStore(config.DefaultState(t.TempDir()))

	// First "session": arm a 07:30 target with a 20m lead-in at 07:12. The
	// fire time (07:10) is two minutes gone, so Arm fires it immediately.
	mt1 := schedule.NewManualTicker()
	alarm1 := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt1 })
	fake1 := &runner.Fake{Result: runner.Result{Text: "first run"}}
	core1 := New(clk, alarm1, fake1, store)

	ctx1, cancel1 := context.WithCancel(context.Background())
	go alarm1.Run(ctx1)
	go core1.Run(ctx1)

	st := store.MustLoad()
	st.Spec = schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute}
	if err := core1.Arm(st); err != nil {
		t.Fatalf("Arm() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for fake1.CallCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fake1.CallCount() != 1 {
		t.Fatalf("setup: claude ran %d times on the immediate fire, want 1", fake1.CallCount())
	}
	cancel1() // "quit" the app

	// The user reopens the app a minute later, at 07:13 -- three minutes past
	// the 07:10 fire time, inside the 5-minute grace window. A second Core,
	// backed by the SAME store, stands in for the new process.
	clk.Advance(1 * time.Minute)

	mt2 := schedule.NewManualTicker()
	alarm2 := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt2 })
	fake2 := &runner.Fake{Result: runner.Result{Text: "second run"}}
	core2 := New(clk, alarm2, fake2, store)

	ctx2, cancel2 := context.WithCancel(context.Background())
	go alarm2.Run(ctx2)
	go core2.Run(ctx2)
	t.Cleanup(cancel2)

	if err := core2.Restore(); err != nil {
		t.Fatal(err)
	}

	// Give a wrongly-firing Restore a moment to land.
	time.Sleep(200 * time.Millisecond)

	if fake2.CallCount() != 0 {
		t.Fatalf("claude ran %d times on restart within the grace window; the first run's disarm was not persisted, "+
			"so Restore replayed the alarm and paid for a second, unrequested invocation", fake2.CallCount())
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

	// Fire time is 07:10, grace is 5m (window closes 07:15). Advance to
	// 10m1s -- ONE SECOND past the fire time, well INSIDE the grace window
	// -- not past it. This is deliberate: at 30m past (as this test
	// previously advanced), even a live Alarm that Disarm forgot to stop
	// would land past its own grace window and report MISSED without
	// running Claude, so the assertions below would pass regardless of
	// whether Disarm actually stopped anything. Landing inside the grace
	// window is what makes a broken Disarm (c.alarm.Disarm() removed from
	// Core.Disarm) actually cause Claude to run, which is the only way this
	// test can catch that regression.
	r.step(10*time.Minute + time.Second)

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

// The alarm is one-shot and never silently reschedules itself (spec §2). A
// suspend that carries the wall clock past the fire time -- even past
// midnight -- must be reported MISSED, not silently re-armed for the next
// occurrence.
//
// The user arms a 23:00 target, then the laptop lid closes for ~20h,
// carrying the wall clock from 07:00 on 07-14 to past 03:00 on 07-15 -- well
// past both the fire time and the 5-minute grace. EventJump is purely
// informational (design doc §5.2/§5.3): Core must not recompute or re-arm
// off it. The correct outcome is MISSED, claude never runs, the alarm is
// disarmed (live and persisted), and -- the key assertion -- it must NOT
// have been silently re-armed for 23:00 the next day.
func TestCoreSuspendPastMidnightReportsMissedAndDoesNotReschedule(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 23, Minute: 0, Offset: 0, Grace: 5 * time.Minute})
	r.drain() // discard the StatusArmed event from arming; isolate the jump's effect

	// The laptop lid closes. Wall clock jumps forward 20h -- past midnight and
	// well past the 23:00 fire time -- while the monotonic clock does not
	// move: a real suspend, per TestClock.Suspend's contract.
	r.clk.Suspend(20 * time.Hour)
	got := r.step(0)

	if !hasStatus(got, StatusMissed) {
		t.Fatalf("want StatusMissed after a suspend that carries past the fire time, got %v", got)
	}
	if r.fake.CallCount() != 0 {
		t.Fatalf("claude ran %d times; a missed alarm must not run", r.fake.CallCount())
	}

	armed, fireAt, _ := r.core.alarm.Armed()
	if armed {
		t.Fatalf("live alarm still armed after MISSED; want disarmed, got fireAt=%v", fireAt)
	}

	st := r.store.MustLoad()
	if st.Armed {
		t.Fatal("persisted state Armed = true after MISSED; want disarmed")
	}

	// The key assertion: no silent re-arm for tomorrow's 23:00.
	notWant := time.Date(2026, 7, 15, 23, 0, 0, 0, time.Local)
	if fireAt.Equal(notWant) {
		t.Fatalf("alarm was silently re-armed for the next day's 23:00 (%v) instead of being reported missed", notWant)
	}
	if st.FireAt.Equal(notWant) {
		t.Fatalf("persisted state was silently re-armed for the next day's 23:00 (%v) instead of being reported missed", notWant)
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

// EventTick used to only ever produce an Event when the alarm was armed, or
// when the status was exactly StatusIdle -- so the clock froze solid for the
// entire duration of a run (StatusRunning, up to 120s). This proves ticks
// keep arriving, marked ClockOnly, while Claude is running.
func TestCoreTicksContinueDuringARunWithoutFreezingTheClock(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)
	r.fake.Delay = 500 * time.Millisecond // hold StatusRunning open

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})

	r.clk.Advance(10 * time.Minute)
	r.tick.Tick()

	// Wait for the genuine StatusRunning event (not a tick) to land -- do not
	// wait for StatusDone, since the fake is deliberately still "running".
	deadline := time.After(2 * time.Second)
runningLoop:
	for {
		select {
		case e := <-r.core.Events():
			if e.Status == StatusRunning && !e.ClockOnly {
				break runningLoop
			}
		case <-deadline:
			t.Fatal("StatusRunning never arrived")
		}
	}

	// Tick once more while the run is still in flight. The old code only
	// ever emitted an Event when armed or exactly StatusIdle -- Running
	// matches neither, so the clock froze here.
	r.clk.Advance(time.Second)
	r.tick.Tick()

	select {
	case e := <-r.core.Events():
		if e.Now.IsZero() {
			t.Fatal("tick during a run carried a zero Now; the clock cannot advance")
		}
		if !e.ClockOnly {
			t.Fatalf("tick during a run must be marked ClockOnly, got %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event delivered for a tick during a run; the clock face has frozen mid-run")
	}
}

// After a terminal status, the poll loop must keep delivering ticks -- the
// clock must not freeze forever just because the alarm already fired -- but
// a tick must never clobber the terminal status/result it is resting on. See
// TestWindowClockOnlyTickDoesNotOverwriteDoneText for the UI-side half of
// this contract.
func TestCoreTicksAfterDoneStillDeliverNowAsClockOnly(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.armed(schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute})
	got := r.step(10 * time.Minute) // fires -> StatusDone
	if !hasStatus(got, StatusDone) {
		t.Fatalf("want StatusDone after firing, got %v", got)
	}

	next := r.step(time.Second)
	if len(next) == 0 {
		t.Fatal("no event delivered on the tick after a terminal status; the clock face has frozen")
	}
	last := next[len(next)-1]
	if last.Now.IsZero() {
		t.Fatal("tick after Done carried a zero Now; the clock cannot advance")
	}
	if !last.ClockOnly {
		t.Fatalf("tick after a terminal status must be marked ClockOnly so the UI cannot re-render it as a fresh "+
			"Done event, got %+v", last)
	}
	if last.Result.Text != "" {
		t.Fatalf("a ClockOnly tick must not carry a stale/zeroed Result, got %+v", last.Result)
	}
}

// waitForCallCount polls fake.CallCount() until it reaches at least n. Unlike
// a fixed sleep, this cannot produce a false pass by guessing too short a
// duration: it only returns once the condition is genuinely true (runner.Fake
// records a call BEFORE blocking on Delay, so CallCount() >= n is proof the
// nth call has entered Run -- not a guess that it probably has by now).
func waitForCallCount(t *testing.T, fake *runner.Fake, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if fake.CallCount() >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for CallCount() to reach %d, got %d", n, fake.CallCount())
		case <-time.After(time.Millisecond):
		}
	}
}

// waitForStatus drains r.core.Events() until a non-ClockOnly event with the
// given status arrives, or fails the test after a generous timeout.
func waitForStatus(t *testing.T, r *rig, want Status) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-r.core.Events():
			if e.Status == want && !e.ClockOnly {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for status %v", want)
		}
	}
}

// Two overlapping invocations of fire -- e.g. a double click on the "Run now"
// button, or an alarm firing while a manual run is still in flight -- must
// not spawn two concurrent, separately billed Claude invocations.
//
// This is made deterministic, not timing-dependent: runner.Fake.Delay holds
// the first call open, and waitForCallCount proves (rather than assumes) that
// the first call has genuinely entered Run -- and therefore that the guard is
// held -- before the second RunNow is issued. A fixed sleep here would be the
// ninth test in this project that could pass or fail by luck rather than by
// the property it claims to check.
func TestCoreFireIgnoresASecondInvocationWhileARunIsInFlight(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)
	r.fake.Delay = 200 * time.Millisecond

	r.core.RunNow()
	waitForCallCount(t, r.fake, 1) // proves the first call holds the guard

	r.core.RunNow() // must be turned away: a run is already in flight

	waitForStatus(t, r, StatusDone) // let the one legitimate run finish

	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times from two overlapping RunNow calls, want 1", r.fake.CallCount())
	}
}

// The guard must not latch permanently. A naive `if c.running { return }`
// with a missed reset on some exit path would pass the test above yet
// silently disable Run now (and every alarm fire) forever after the first
// invocation -- a regression far worse than the double-fire bug it was meant
// to prevent. This proves the guard releases: a second, later, non-concurrent
// RunNow after the first genuinely completes must still invoke Claude.
func TestCoreFireAcceptsANewRunAfterThePreviousOneCompletes(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	r := newRig(t, start)

	r.core.RunNow()
	waitForStatus(t, r, StatusDone)
	if r.fake.CallCount() != 1 {
		t.Fatalf("claude ran %d times on the first RunNow, want 1", r.fake.CallCount())
	}

	r.core.RunNow()
	waitForStatus(t, r, StatusDone)
	if r.fake.CallCount() != 2 {
		t.Fatalf("claude ran %d times after two sequential, non-overlapping RunNow calls, want 2: "+
			"the guard must release once a run completes, not latch forever", r.fake.CallCount())
	}
}
