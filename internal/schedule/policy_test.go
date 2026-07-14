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

// Decide must compare wall clock to wall clock. A time.Time carrying a
// monotonic reading must not be treated differently from one that has had it
// stripped -- if it is, we have reintroduced the suspend bug inside a function
// that looks like it compares wall clocks.
func TestDecideIgnoresMonotonicReading(t *testing.T) {
	// time.Now() carries a monotonic reading; Round(0) strips it.
	withMono := time.Now()
	stripped := withMono.Round(0)

	fire := stripped.Add(-time.Minute) // one minute overdue, inside a 5m grace

	got1 := Decide(withMono, fire, 5*time.Minute)
	got2 := Decide(stripped, fire, 5*time.Minute)

	if got1 != got2 {
		t.Fatalf("Decide disagrees depending on monotonic reading: %v vs %v", got1, got2)
	}
	if got1 != DecideFire {
		t.Fatalf("Decide = %v, want DecideFire", got1)
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
