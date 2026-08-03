package schedule

import (
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	fire := time.Date(2026, 7, 14, 7, 10, 0, 0, time.UTC)
	const grace = 5 * time.Minute

	tests := []struct {
		name string
		now  time.Time
		want Decision
	}{
		{"long before", fire.Add(-time.Hour), DecideWait},
		{"one second before", fire.Add(-time.Second), DecideWait},
		{"exactly on time", fire, DecideFire},
		{"one second late", fire.Add(time.Second), DecideFire},
		{"just inside grace", fire.Add(grace - time.Second), DecideFire},
		{"exactly at grace edge", fire.Add(grace), DecideFire},
		{"one second past grace", fire.Add(grace + time.Second), DecideMissed},
		{"hours past grace", fire.Add(3 * time.Hour), DecideMissed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.now, fire, grace); got != tt.want {
				t.Fatalf("Decide(%v) = %v, want %v", tt.now, got, tt.want)
			}
		})
	}
}

// A zero grace means the alarm may only fire on the exact tick it is due.
func TestDecideZeroGrace(t *testing.T) {
	fire := time.Date(2026, 7, 14, 7, 10, 0, 0, time.UTC)

	if got := Decide(fire, fire, 0); got != DecideFire {
		t.Fatalf("Decide at exactly fire with zero grace = %v, want DecideFire", got)
	}
	if got := Decide(fire.Add(time.Second), fire, 0); got != DecideMissed {
		t.Fatalf("Decide 1s late with zero grace = %v, want DecideMissed", got)
	}
}

// TestDecideMonotonicAgreement checks that Decide returns the same answer no
// matter which of now and fireAt carry a monotonic reading. It exercises all
// four combinations: mono/mono, mono/stripped, stripped/mono, and
// stripped/stripped.
//
// This is a WEAKER property than "immune to a suspend": it only shows that,
// for two time.Time values that are not actually diverged, Decide's answer
// does not depend on which of them happen to carry a monotonic reading. It
// does not -- and cannot -- exercise the case Round(0) actually defends
// against, which is a real suspend or clock step causing the monotonic and
// wall clocks to disagree. Go's monotonic reading is only ever advanced by
// the OS scheduler; there is no public API to fabricate a diverged one, and
// no in-process test can force a kernel suspend. So this test cannot fail by
// deleting the Round(0) calls in Decide: without a real divergence, the wall
// and monotonic deltas between now and fireAt are identical in this process,
// so every combination agrees regardless of which clock backs the
// comparison. That stronger guarantee is out of reach for a unit test and is
// verified instead by the real-suspend item in the manual test checklist.
//
// What this test does catch: any change to Decide that makes its result
// depend on operand representation rather than on the time instants
// themselves (e.g. an asymmetric fix that rounds one operand but not the
// other) would still leave this test green today, for the same reason above
// -- so treat it as documentation of the intended invariant, not as a
// regression guard for the suspend bug.
func TestDecideMonotonicAgreement(t *testing.T) {
	// time.Now() carries a monotonic reading; Round(0) strips it.
	monoNow := time.Now()
	monoFire := monoNow.Add(-time.Minute) // one minute overdue, inside a 5m grace
	strippedNow := monoNow.Round(0)
	strippedFire := monoFire.Round(0)

	const grace = 5 * time.Minute

	combos := []struct {
		name string
		now  time.Time
		fire time.Time
	}{
		{"mono now / mono fireAt", monoNow, monoFire},
		{"mono now / stripped fireAt", monoNow, strippedFire},
		{"stripped now / mono fireAt", strippedNow, monoFire},
		{"stripped now / stripped fireAt", strippedNow, strippedFire},
	}

	want := Decide(combos[0].now, combos[0].fire, grace)
	for _, c := range combos {
		if got := Decide(c.now, c.fire, grace); got != want {
			t.Errorf("Decide(%s) = %v, want %v (agreement with mono/mono case)", c.name, got, want)
		}
	}
	if want != DecideFire {
		t.Fatalf("baseline Decide = %v, want DecideFire", want)
	}
}

func TestDetectJump(t *testing.T) {
	const period = time.Second

	tests := []struct {
		name       string
		wallDelta  time.Duration
		monoDelta  time.Duration
		wantJumped bool
		wantJump   time.Duration
	}{
		{
			name:      "normal tick: wall and monotonic agree",
			wallDelta: time.Second, monoDelta: time.Second,
			wantJumped: false,
		},
		{
			name:      "small scheduling jitter is not a jump",
			wallDelta: 1100 * time.Millisecond, monoDelta: 1050 * time.Millisecond,
			wantJumped: false,
		},
		{
			// The machine slept for 12h30m: the wall clock advanced, the
			// monotonic clock did not. This is the suspend signature.
			name:      "suspend: wall advances, monotonic does not",
			wallDelta: 12*time.Hour + 30*time.Minute, monoDelta: time.Second,
			wantJumped: true,
			wantJump:   12*time.Hour + 30*time.Minute - time.Second,
		},
		{
			name:      "NTP steps the clock backwards",
			wallDelta: -30 * time.Second, monoDelta: time.Second,
			wantJumped: true,
			wantJump:   -31 * time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jump, jumped := DetectJump(tt.wallDelta, tt.monoDelta, period)
			if jumped != tt.wantJumped {
				t.Fatalf("DetectJump jumped = %v, want %v", jumped, tt.wantJumped)
			}
			if jumped && jump != tt.wantJump {
				t.Fatalf("DetectJump jump = %v, want %v", jump, tt.wantJump)
			}
		})
	}
}
