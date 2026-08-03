package ui

import (
	"testing"
	"time"
)

// The lead-in is typed on the same HH:MM face as the target time above it:
// "03:00" is three hours, "02:30" is two and a half. Bare minute counts are
// what this replaced -- "180" must no longer be a way to say three hours,
// because it is exactly the reading the field was changed to avoid.
func TestParseLeadIn(t *testing.T) {
	accept := []struct {
		in   string
		want time.Duration
	}{
		{"03:00", 3 * time.Hour},
		{"02:30", 2*time.Hour + 30*time.Minute},
		{"00:05", 5 * time.Minute},
		{"00:00", 0},
		{"23:59", 23*time.Hour + 59*time.Minute},
	}
	for _, c := range accept {
		got, err := parseLeadIn(c.in)
		if err != nil {
			t.Errorf("parseLeadIn(%q) = error %v, want %v", c.in, err, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("parseLeadIn(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	reject := []string{
		"180",   // the old bare-minutes spelling
		"3",     // ambiguous: three hours or three minutes?
		"3:00",  // un-padded, exactly as the target-time field rejects it
		"24:00", // >= 24h, which schedule.Spec.Validate forbids
		"00:60",
		"-01:00",
		"abc",
		"",
	}
	for _, s := range reject {
		if got, err := parseLeadIn(s); err == nil {
			t.Errorf("parseLeadIn(%q) = %v, want an error", s, got)
		}
	}
}

// formatLeadIn seeds the entry from the persisted duration, so it must produce
// something parseLeadIn accepts -- zero-padded on both halves.
func TestFormatLeadIn(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{3 * time.Hour, "03:00"},
		{150 * time.Minute, "02:30"},
		{5 * time.Minute, "00:05"},
		{0, "00:00"},
		{23*time.Hour + 59*time.Minute, "23:59"},
	}
	for _, c := range cases {
		if got := formatLeadIn(c.in); got != c.want {
			t.Errorf("formatLeadIn(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A negative offset is unreachable through schedule.Spec.Validate, but
// formatLeadIn is fed straight from persisted config and must not render one
// as the nonsense "-1:-30". It clamps, like formatCountdown and inDuration.
func TestFormatLeadInClampsNegative(t *testing.T) {
	if got := formatLeadIn(-90 * time.Minute); got != "00:00" {
		t.Fatalf("formatLeadIn(-1h30m) = %q, want %q", got, "00:00")
	}
}

// The two must agree: whatever formatLeadIn puts in the entry, parseLeadIn has
// to read back as the same duration, or the field would seed itself into an
// invalid state.
func TestFormatLeadInRoundTripsThroughParseLeadIn(t *testing.T) {
	for _, d := range []time.Duration{0, time.Minute, 5 * time.Minute, 90 * time.Minute, 3 * time.Hour, 23*time.Hour + 59*time.Minute} {
		s := formatLeadIn(d)
		got, err := parseLeadIn(s)
		if err != nil {
			t.Errorf("parseLeadIn(formatLeadIn(%v)) = error %v", d, err)
			continue
		}
		if got != d {
			t.Errorf("parseLeadIn(formatLeadIn(%v)) = %v, want the original", d, got)
		}
	}
}
