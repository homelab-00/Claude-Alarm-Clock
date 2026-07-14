package schedule

import (
	"context"
	"testing"
	"time"
)

// harness wires an Alarm to a TestClock and a ManualTicker, and gives the test
// a single-tick-and-collect primitive.
type harness struct {
	t     *testing.T
	clk   *TestClock
	tick  *ManualTicker
	alarm *Alarm
	stop  context.CancelFunc
}

func newHarness(t *testing.T, start time.Time) *harness {
	t.Helper()

	clk := NewTestClock(start)
	mt := NewManualTicker()
	a := NewAlarm(clk, time.Second, func(time.Duration) Ticker { return mt })

	ctx, cancel := context.WithCancel(context.Background())
	go a.Run(ctx)
	t.Cleanup(cancel)

	return &harness{t: t, clk: clk, tick: mt, alarm: a, stop: cancel}
}

// step advances the clock (wall AND monotonic, exactly like ordinary time
// passing -- or a late/starved tick) by d, delivers one tick, and returns
// every Update the loop emitted for it.
func (h *harness) step(d time.Duration) []Update {
	h.t.Helper()
	h.clk.Advance(d)
	h.tick.Tick()
	return h.collect()
}

// stepSuspend simulates a machine suspend of duration d: the wall clock jumps
// forward by d while the monotonic clock does not move at all, exactly what
// TestClock.Suspend models a sleeping laptop as. It then delivers one tick
// and returns every Update the loop emitted for it.
func (h *harness) stepSuspend(d time.Duration) []Update {
	h.t.Helper()
	h.clk.Suspend(d)
	h.tick.Tick()
	return h.collect()
}

// collect drains every Update the loop has ready to emit for the tick just
// delivered.
func (h *harness) collect() []Update {
	h.t.Helper()
	var got []Update
	for {
		select {
		case u := <-h.alarm.Updates():
			got = append(got, u)
		case <-time.After(200 * time.Millisecond):
			return got
		}
	}
}

func kinds(us []Update) []EventKind {
	ks := make([]EventKind, len(us))
	for i, u := range us {
		ks[i] = u.Kind
	}
	return ks
}

func hasKind(us []Update, k EventKind) bool {
	for _, u := range us {
		if u.Kind == k {
			return true
		}
	}
	return false
}

// A disarmed alarm still ticks -- that is what drives the UI clock face.
func TestAlarmTicksWhenDisarmed(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	got := h.step(time.Second)

	if len(got) != 1 || got[0].Kind != EventTick {
		t.Fatalf("kinds = %v, want exactly [tick]", kinds(got))
	}
	if want := start.Add(time.Second); !got[0].Now.Equal(want) {
		t.Fatalf("Now = %v, want %v", got[0].Now, want)
	}
}

func TestAlarmCountsDownThenFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(3 * time.Second)
	target := start.Add(1 * time.Hour)
	h.alarm.Arm(fire, target, 5*time.Minute)

	// Two ticks short of the fire time: countdown only.
	for i := 1; i <= 2; i++ {
		got := h.step(time.Second)
		if len(got) != 1 || got[0].Kind != EventTick {
			t.Fatalf("tick %d: kinds = %v, want [tick]", i, kinds(got))
		}
		wantRemaining := time.Duration(3-i) * time.Second
		if got[0].Remaining != wantRemaining {
			t.Fatalf("tick %d: Remaining = %v, want %v", i, got[0].Remaining, wantRemaining)
		}
	}

	// Third tick lands exactly on the fire time.
	got := h.step(time.Second)
	if !hasKind(got, EventFire) {
		t.Fatalf("kinds = %v, want a fire", kinds(got))
	}

	// And it disarms itself, so it cannot fire twice.
	if armed, _, _ := h.alarm.Armed(); armed {
		t.Fatal("alarm still armed after firing")
	}
	if got := h.step(time.Second); hasKind(got, EventFire) {
		t.Fatalf("fired a second time: kinds = %v", kinds(got))
	}
}

// THE test. This is the one that would have caught the monotonic-clock bug.
//
// The machine suspends for 12h30m across the fire time. On resume the alarm is
// hours stale, so it must report MISSED -- not fire, and not fire late.
//
// Single-tick: EventJump is purely informational and does not suppress this
// tick's own Decide against fireAt. fireAt is an absolute instant, unaffected
// by the jump, so the same tick that reports the jump also correctly reports
// MISSED. See §5.2/§5.3 of the design doc.
func TestAlarmSuspendPastGraceReportsMissedAndDoesNotFire(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(10 * time.Minute)
	target := fire.Add(20 * time.Minute)
	grace := 5 * time.Minute
	h.alarm.Arm(fire, target, grace)

	// The laptop lid closes. One tick later, 12h30m of wall clock has passed
	// but the monotonic clock has not moved at all -- a real suspend.
	got := h.stepSuspend(12*time.Hour + 30*time.Minute)

	if !hasKind(got, EventJump) {
		t.Fatalf("kinds = %v, want a jump event", kinds(got))
	}
	if !hasKind(got, EventMissed) {
		t.Fatalf("kinds = %v, want a missed event on the same tick as the jump", kinds(got))
	}
	if hasKind(got, EventFire) {
		t.Fatalf("alarm FIRED after a 12h30m suspend; it must not: kinds = %v", kinds(got))
	}

	for _, u := range got {
		if u.Kind == EventMissed {
			want := 12*time.Hour + 20*time.Minute // 12h30m elapsed, fire was 10m in
			if u.Late != want {
				t.Fatalf("Late = %v, want %v", u.Late, want)
			}
		}
	}

	if armed, _, _ := h.alarm.Armed(); armed {
		t.Fatal("alarm still armed after being missed")
	}
}

// A short suspend that lands inside the grace window must still fire.
//
// Single-tick, for the same reason as TestAlarmSuspendPastGraceReportsMissedAndDoesNotFire
// above: the jump tick itself both reports the jump and correctly Decides.
func TestAlarmSuspendWithinGraceStillFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(1 * time.Minute)
	target := fire.Add(20 * time.Minute)
	grace := 5 * time.Minute
	h.alarm.Arm(fire, target, grace)

	// Suspend for 4 minutes: 3 minutes past the fire time, inside a 5m grace.
	got := h.stepSuspend(4 * time.Minute)

	if !hasKind(got, EventJump) {
		t.Fatalf("kinds = %v, want a jump event", kinds(got))
	}
	if !hasKind(got, EventFire) {
		t.Fatalf("kinds = %v, want a fire (3m late is inside a 5m grace) on the same tick as the jump", kinds(got))
	}
	if hasKind(got, EventMissed) {
		t.Fatalf("kinds = %v, want no missed", kinds(got))
	}
}

// A merely late tick -- the process was starved of CPU time, not suspended --
// must NOT be reported as a clock jump. Wall and monotonic time both still
// advance together here (h.step uses TestClock.Advance, not Suspend); only
// their DIVERGENCE should ever trigger EventJump.
//
// This is the test that distinguishes a correct wall-vs-monotonic comparison
// from the workaround it replaces, which substituted the ticker's nominal
// period for the (unavailable, at the time) monotonic delta: DetectJump(wallDelta,
// period, period) sees a 10s wallDelta against a 1s period on this harness's
// 1-second ticker and false-positives a jump, even though nothing suspended.
func TestAlarmLateTickIsNotMistakenForSuspend(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start) // period is 1 second, see newHarness

	// A starved process might go 10 periods between scheduler slices, but
	// wall and monotonic clocks agree on how much time passed regardless.
	got := h.step(10 * time.Second)

	if hasKind(got, EventJump) {
		t.Fatalf("kinds = %v, want no jump for a merely late (non-suspend) tick", kinds(got))
	}
}

func TestAlarmDisarmStopsItFiring(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(2 * time.Second)
	h.alarm.Arm(fire, fire, time.Minute)
	h.alarm.Disarm()

	got := h.step(10 * time.Second)

	if hasKind(got, EventFire) {
		t.Fatalf("disarmed alarm fired: kinds = %v", kinds(got))
	}
	if !hasKind(got, EventTick) {
		t.Fatalf("kinds = %v, want it still ticking after disarm", kinds(got))
	}
}

// Re-arming replaces the pending alarm. The old fire time must be forgotten --
// this is the race that a spawn-per-alarm design needs a generation counter for,
// and that a single loop makes structurally impossible.
func TestAlarmRearmReplacesPendingFireTime(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	h.alarm.Arm(start.Add(2*time.Second), start.Add(time.Hour), time.Minute)
	h.alarm.Arm(start.Add(9*time.Second), start.Add(2*time.Hour), time.Minute)

	// Past the FIRST fire time. It must not fire: that alarm no longer exists.
	if got := h.step(3 * time.Second); hasKind(got, EventFire) {
		t.Fatalf("fired at the superseded fire time: kinds = %v", kinds(got))
	}

	// Past the second. Exactly one fire.
	got := h.step(7 * time.Second)
	fires := 0
	for _, u := range got {
		if u.Kind == EventFire {
			fires++
		}
	}
	if fires != 1 {
		t.Fatalf("got %d fires, want exactly 1: kinds = %v", fires, kinds(got))
	}
}

func TestAlarmRunStopsOnContextCancel(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	clk := NewTestClock(start)
	mt := NewManualTicker()
	a := NewAlarm(clk, time.Second, func(time.Duration) Ticker { return mt })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of context cancellation")
	}
}

// -- Supplementary coverage tests below this line --
//
// Everything above this point is the verbatim brief test suite, in its
// natural single-tick shape: a jump tick both reports EventJump and Decides
// on fireAt (which the jump does not invalidate -- it is an absolute
// instant). These two below are additions, in scope only because they cover
// alarm.go code this task introduces (EventKind.String, and emit's
// ctx-cancellation path) that the original suite does not happen to reach.

// TestAlarmRearmDuringFireIsNotClobbered reproduces the TOCTOU lost-update
// race: Run reads state (snapshot), releases the lock, calls the pure
// Decide, and only THEN re-acquires the lock to clear the alarm. If a
// concurrent Arm from another goroutine (the UI thread, in production) lands
// in that gap, an unconditional clear would silently throw the NEW alarm
// away -- Armed() would report false, no event would ever be emitted for it,
// and the user would get no error.
//
// There is no channel operation in that gap to block on naturally, so the
// interleaving cannot be forced by ordinary goroutine scheduling -- hitting
// it would require relying on preemption, which is exactly the kind of
// unfalsifiable, timing-based test this project has already rejected twice.
// Instead this test uses the beforeDisarm hook to pause the loop deterministically
// exactly inside the gap: the loop reaches beforeDisarm only after Decide has
// already concluded DecideFire, and it cannot proceed to disarmIfStill until
// this test releases it. The concurrent Arm is issued while the loop is
// provably paused there, not merely "probably" paused there.
func TestAlarmRearmDuringFireIsNotClobbered(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	clk := NewTestClock(start)
	mt := NewManualTicker()
	a := NewAlarm(clk, time.Second, func(time.Duration) Ticker { return mt })

	oldFire := start.Add(2 * time.Second)
	oldTarget := start.Add(time.Hour)
	a.Arm(oldFire, oldTarget, time.Minute)

	newFire := start.Add(20 * time.Second)
	newTarget := start.Add(2 * time.Hour)

	reachedGap := make(chan struct{})
	proceed := make(chan struct{})
	// Set before `go a.Run(ctx)`: the happens-before guarantee of the go
	// statement is what makes this safe to read from Run's goroutine without
	// a mutex (see the field's doc comment in alarm.go).
	a.beforeDisarm = func() {
		close(reachedGap)
		<-proceed
		// Self-clear so the SECOND fire (of the new alarm, later in this
		// test) runs the unmodified fast path instead of re-entering this
		// hook. Safe without a mutex: only Run's own goroutine ever calls or
		// clears this field once Run has started.
		a.beforeDisarm = nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go a.Run(ctx)

	clk.Advance(2 * time.Second) // now lands exactly on oldFire
	mt.Tick()                    // returns once the loop's select receives it

	<-reachedGap // the loop has decided DecideFire for oldFire and is paused

	// The re-arm, issued while the loop is provably stuck between deciding
	// to fire and clearing -- the exact TOCTOU window.
	a.Arm(newFire, newTarget, time.Minute)

	close(proceed) // release the loop to attempt disarmIfStill(oldFire)

	// The old, superseded fire must NOT be emitted: the alarm it referred to
	// no longer exists by the time the loop resumed.
	select {
	case u := <-a.Updates():
		t.Fatalf("got an emitted update for the superseded old alarm: %+v", u)
	case <-time.After(200 * time.Millisecond):
	}

	// The new alarm, armed mid-race, must have survived intact.
	armed, fireAt, target := a.Armed()
	if !armed {
		t.Fatal("alarm disarmed after a concurrent re-arm during a fire; the new alarm was lost")
	}
	if !fireAt.Equal(newFire) || !target.Equal(newTarget) {
		t.Fatalf("Armed() = (%v, %v), want the NEW alarm (%v, %v)", fireAt, target, newFire, newTarget)
	}

	// And it must still fire, on its own schedule.
	clk.Advance(18 * time.Second) // now lands exactly on newFire
	mt.Tick()

	select {
	case u := <-a.Updates():
		if u.Kind != EventFire || !u.FireAt.Equal(newFire) || !u.Target.Equal(newTarget) {
			t.Fatalf("got %+v, want EventFire at %v for %v", u, newFire, newTarget)
		}
	case <-time.After(time.Second):
		t.Fatal("the new alarm never fired")
	}
}

func TestEventKindString(t *testing.T) {
	tests := []struct {
		k    EventKind
		want string
	}{
		{EventTick, "tick"},
		{EventFire, "fire"},
		{EventMissed, "missed"},
		{EventJump, "jump"},
		{EventKind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.k.String(); got != tt.want {
			t.Errorf("EventKind(%d).String() = %q, want %q", tt.k, got, tt.want)
		}
	}
}

// If nobody is reading Updates(), the loop blocks trying to emit. Cancelling
// ctx must still unblock and return it -- this is the same guarantee
// TestAlarmRunStopsOnContextCancel checks before any tick, exercised here
// while a send is actually in flight.
func TestAlarmUnblocksFromEmitOnContextCancel(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	clk := NewTestClock(start)
	mt := NewManualTicker()
	a := NewAlarm(clk, time.Second, func(time.Duration) Ticker { return mt })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx); close(done) }()

	// Deliver a tick but never read a.Updates(): the loop blocks trying to
	// send it. Tick() itself returns as soon as the loop's select receives it,
	// regardless of what the loop does next.
	clk.Advance(time.Second)
	mt.Tick()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return within 1s of context cancellation while blocked emitting")
	}
}
