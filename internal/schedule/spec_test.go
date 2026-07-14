package schedule

import (
	"testing"
	"time"
)

func athens(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return loc
}

func TestNextTargetTodayIfStillFuture(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 14, 6, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, 7, 14, 7, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextTarget = %v, want %v", got, want)
	}
}

func TestNextTargetRollsToTomorrowIfPassed(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 14, 9, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, 7, 15, 7, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextTarget = %v, want %v", got, want)
	}
}

// A target exactly equal to now has passed. Roll it.
func TestNextTargetExactlyNowRollsToTomorrow(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 14, 7, 30, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	if got.Day() != 15 {
		t.Fatalf("NextTarget = %v, want 15 July", got)
	}
}

// Month boundaries must normalise, not overflow.
func TestNextTargetCrossesMonthEnd(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 6, Minute: 0, Zone: "Europe/Athens"}

	from := time.Date(2026, 7, 31, 23, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	want := time.Date(2026, 8, 1, 6, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("NextTarget = %v, want %v", got, want)
	}
}

func TestFireAtSubtractsOffset(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens", Offset: 20 * time.Minute}

	from := time.Date(2026, 7, 14, 6, 0, 0, 0, loc)
	fire, target, err := s.FireAt(from)
	if err != nil {
		t.Fatal(err)
	}

	if want := time.Date(2026, 7, 14, 7, 30, 0, 0, loc); !target.Equal(want) {
		t.Fatalf("target = %v, want %v", target, want)
	}
	if want := time.Date(2026, 7, 14, 7, 10, 0, 0, loc); !fire.Equal(want) {
		t.Fatalf("fire = %v, want %v", fire, want)
	}
}

// THE test that justifies Add(-offset) on the instant.
//
// On 2026-03-29 Athens jumps 03:00 EET (+02) -> 04:00 EEST (+03). The hour from
// 03:00 to 04:00 does not exist.
//
// For an 04:30 target with a 1-hour lead-in, naive minute arithmetic gives a
// fire time of 03:30 -- an instant that never happens. Subtracting the offset
// from the *instant* instead gives 02:30 EET: two hours earlier on the wall
// clock, but exactly one hour of real elapsed time before the target, which is
// what the user actually asked for.
func TestFireAtOffsetSpansSpringForwardGap(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 4, Minute: 30, Zone: "Europe/Athens", Offset: time.Hour}

	from := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	fire, target, err := s.FireAt(from)
	if err != nil {
		t.Fatal(err)
	}

	// The invariant that matters: exactly one hour of real time elapses.
	if d := target.Sub(fire); d != time.Hour {
		t.Fatalf("target.Sub(fire) = %v, want exactly 1h", d)
	}
	// And it lands two hours earlier on the wall clock, because an hour vanished.
	if got := fire.Format("15:04"); got != "02:30" {
		t.Fatalf("fire wall clock = %s, want 02:30", got)
	}
	if got := target.Format("15:04"); got != "04:30" {
		t.Fatalf("target wall clock = %s, want 04:30", got)
	}
}

// A target inside the spring-forward gap does not exist. Go's time.Date slides
// it forward an hour. We adopt that as our policy; this test pins it so it
// cannot change underneath us silently.
func TestNextTargetInsideSpringForwardGapSlidesForward(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 3, Minute: 30, Zone: "Europe/Athens"} // does not exist on 2026-03-29

	from := time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	if got.Hour() != 4 || got.Minute() != 30 {
		t.Fatalf("NextTarget = %v, want it slid to 04:30", got)
	}
	if !got.After(from) {
		t.Fatalf("NextTarget = %v is not after %v", got, from)
	}
	if got.Day() != 29 {
		t.Fatalf("NextTarget = %v, want it still on the 29th", got)
	}
}

// On 2026-10-25 Athens falls back 04:00 EEST -> 03:00 EET, so 03:30 happens
// twice. Go picks one; the docs do not guarantee which. We do not over-fit to
// that choice -- we assert only the invariants we actually depend on: it is a
// real instant, it is in the future, and its wall clock reads 03:30.
func TestNextTargetInsideFallBackAmbiguityIsStillValid(t *testing.T) {
	loc := athens(t)
	s := Spec{Hour: 3, Minute: 30, Zone: "Europe/Athens"}

	from := time.Date(2026, 10, 25, 0, 0, 0, 0, loc)
	got, err := s.NextTarget(from)
	if err != nil {
		t.Fatal(err)
	}

	if !got.After(from) {
		t.Fatalf("NextTarget = %v is not after %v", got, from)
	}
	if w := got.Format("15:04"); w != "03:30" {
		t.Fatalf("NextTarget wall clock = %s, want 03:30", w)
	}
	if got.Day() != 25 {
		t.Fatalf("NextTarget = %v, want it on the 25th", got)
	}
}

// An empty Zone means "system local", resolved at call time.
func TestEmptyZoneMeansLocal(t *testing.T) {
	s := Spec{Hour: 7, Minute: 30}
	loc, err := s.Location()
	if err != nil {
		t.Fatal(err)
	}
	if loc != time.Local {
		t.Fatalf("Location() = %v, want time.Local", loc)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"ok", Spec{Hour: 7, Minute: 30, Offset: time.Minute, Grace: time.Minute}, false},
		{"midnight ok", Spec{Hour: 0, Minute: 0}, false},
		{"last minute ok", Spec{Hour: 23, Minute: 59}, false},
		{"hour too big", Spec{Hour: 24}, true},
		{"hour negative", Spec{Hour: -1}, true},
		{"minute too big", Spec{Minute: 60}, true},
		{"minute negative", Spec{Minute: -1}, true},
		{"negative offset", Spec{Offset: -time.Minute}, true},
		{"negative grace", Spec{Grace: -time.Minute}, true},
		{"offset >= 24h", Spec{Offset: 24 * time.Hour}, true},
		{"bad zone", Spec{Zone: "Mars/Olympus_Mons"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseHHMM(t *testing.T) {
	tests := []struct {
		in      string
		wantH   int
		wantM   int
		wantErr bool
	}{
		{"07:30", 7, 30, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"7:30", 0, 0, true}, // must be zero-padded
		{"24:00", 0, 0, true},
		{"07:60", 0, 0, true},
		{"", 0, 0, true},
		{"0730", 0, 0, true},
		{"07:30:00", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			h, m, err := ParseHHMM(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseHHMM(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && (h != tt.wantH || m != tt.wantM) {
				t.Fatalf("ParseHHMM(%q) = %d,%d want %d,%d", tt.in, h, m, tt.wantH, tt.wantM)
			}
		})
	}
}
