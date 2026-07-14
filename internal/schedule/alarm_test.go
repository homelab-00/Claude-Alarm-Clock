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

// step advances the clock by d, delivers one tick, and returns every Update the
// loop emitted for it.
func (h *harness) step(d time.Duration) []Update {
	h.t.Helper()
	h.clk.Advance(d)
	h.tick.Tick()

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
func TestAlarmSuspendPastGraceReportsMissedAndDoesNotFire(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(10 * time.Minute)
	h.alarm.Arm(fire, fire.Add(20*time.Minute), 5*time.Minute)

	// The laptop lid closes. One tick later, 12h30m of wall clock has passed.
	got := h.step(12*time.Hour + 30*time.Minute)

	if !hasKind(got, EventJump) {
		t.Fatalf("kinds = %v, want a jump event", kinds(got))
	}
	if !hasKind(got, EventMissed) {
		t.Fatalf("kinds = %v, want a missed event", kinds(got))
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
func TestAlarmSuspendWithinGraceStillFires(t *testing.T) {
	start := time.Date(2026, 7, 14, 7, 0, 0, 0, time.UTC)
	h := newHarness(t, start)

	fire := start.Add(1 * time.Minute)
	h.alarm.Arm(fire, fire.Add(20*time.Minute), 5*time.Minute)

	// Suspend for 4 minutes: 3 minutes past the fire time, inside a 5m grace.
	got := h.step(4 * time.Minute)

	if !hasKind(got, EventFire) {
		t.Fatalf("kinds = %v, want a fire (3m late is inside a 5m grace)", kinds(got))
	}
	if hasKind(got, EventMissed) {
		t.Fatalf("kinds = %v, want no missed", kinds(got))
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
// Everything above this point is the verbatim brief test suite and must not
// be altered. These two are additions, in scope only because they cover
// alarm.go code this task introduces (EventKind.String, and emit's
// ctx-cancellation path) that the verbatim suite does not happen to reach.

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
