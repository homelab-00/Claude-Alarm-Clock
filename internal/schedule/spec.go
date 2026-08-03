package schedule

import (
	"fmt"
	"time"
)

// Spec is the user's intent, and it is what gets persisted.
//
// We deliberately do NOT persist an absolute time.Time. An instant is
// meaningless across a reboot, a DST change, or a timezone change; the intent
// ("07:30 in Athens, 20 minutes early") survives all three.
type Spec struct {
	Hour   int           // 0-23
	Minute int           // 0-59
	Zone   string        // IANA name, e.g. "Europe/Athens". Empty means system local.
	Offset time.Duration // lead-in: fire this long before the target
	Grace  time.Duration // how stale an unobserved alarm may be and still fire
}

// Validate reports whether the Spec is well-formed.
func (s Spec) Validate() error {
	if s.Hour < 0 || s.Hour > 23 {
		return fmt.Errorf("hour %d out of range 0-23", s.Hour)
	}
	if s.Minute < 0 || s.Minute > 59 {
		return fmt.Errorf("minute %d out of range 0-59", s.Minute)
	}
	if s.Offset < 0 {
		return fmt.Errorf("offset %v is negative", s.Offset)
	}
	if s.Offset >= 24*time.Hour {
		return fmt.Errorf("offset %v must be less than 24h", s.Offset)
	}
	if s.Grace < 0 {
		return fmt.Errorf("grace %v is negative", s.Grace)
	}
	if _, err := s.Location(); err != nil {
		return err
	}
	return nil
}

// Location resolves the Spec's timezone.
//
// We re-resolve on every call rather than caching. time.Local is resolved once
// per process (sync.Once), so a tray app running for days would otherwise never
// notice the user changing their system timezone.
func (s Spec) Location() (*time.Location, error) {
	if s.Zone == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(s.Zone)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", s.Zone, err)
	}
	return loc, nil
}

// NextTarget returns the next occurrence of Hour:Minute strictly after from --
// today if it is still ahead, otherwise tomorrow.
//
// Day()+1 is safe: time.Date normalises out-of-range fields, so 31 July + 1
// becomes 1 August.
//
// DST policy: for a target inside a spring-forward gap, time.Date slides it
// forward by an hour; for an ambiguous fall-back time it picks one instance.
// We adopt Go's behaviour as our policy, and spec_test.go pins it.
func (s Spec) NextTarget(from time.Time) (time.Time, error) {
	loc, err := s.Location()
	if err != nil {
		return time.Time{}, err
	}
	from = from.In(loc)

	t := time.Date(from.Year(), from.Month(), from.Day(), s.Hour, s.Minute, 0, 0, loc)
	if !t.After(from) {
		t = time.Date(from.Year(), from.Month(), from.Day()+1, s.Hour, s.Minute, 0, 0, loc)
	}
	return t, nil
}

// FireAt returns the instant the alarm should fire, and the target it precedes.
//
// The offset is subtracted from the *instant*, not from the calendar fields.
// That is what keeps the lead-in a true duration when it straddles a DST
// boundary: an offset of one hour always means one hour of real elapsed time,
// even if the wall clock appears to move by two.
func (s Spec) FireAt(from time.Time) (fire, target time.Time, err error) {
	target, err = s.NextTarget(from)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return target.Add(-s.Offset), target, nil
}

// ParseHHMM parses a zero-padded 24-hour "15:04" string.
//
// time.Parse alone is not enough: Go's numeric parsing is lenient about
// leading zeros (it happily accepts "7:30" for the "15:04" layout), but we
// require strict zero-padding. Reformatting the parsed result and comparing
// it back against the input catches that case without hand-rolling a parser.
func ParseHHMM(s string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, 0, fmt.Errorf("time must be HH:MM, e.g. 07:30: %w", err)
	}
	if t.Format("15:04") != s {
		return 0, 0, fmt.Errorf("time must be zero-padded HH:MM, e.g. 07:30: got %q", s)
	}
	return t.Hour(), t.Minute(), nil
}
