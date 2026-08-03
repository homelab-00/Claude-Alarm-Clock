package ui

import (
	"errors"
	"fmt"
	"time"

	"claudealarm/internal/schedule"
)

// The lead-in ("Run this early") is a duration, but it is typed and displayed
// on the same zero-padded HH:MM face as the target time directly above it:
// "03:00" is three hours, "02:30" is two and a half. It used to be a bare
// minute count, which made a three-hour lead-in read as "180" -- a number the
// user has to divide in their head before it means anything.
//
// Reusing schedule.ParseHHMM here is not just convenience. The lead-in's legal
// range is exactly 0 <= offset < 24h (schedule.Spec.Validate), which is exactly
// the range ParseHHMM already accepts. So the old validator's two hand-rolled
// bound checks -- "cannot be negative", "must be less than 24 hours" -- are not
// reimplemented below; they are unreachable. It also keeps the two fields
// equally strict about zero-padding, so "3:00" is rejected in the lead-in for
// the same reason "7:30" is rejected in the target time.

// parseLeadIn reads the lead-in entry. It is the single source of truth for
// that field: the widget's validator, the live explainer and Arm all call it,
// so the field can never show "valid" for input Arm would then reject.
//
// The error wording is a sentence fragment on purpose -- callers prefix it with
// the field's own name ("Run this early ...", "lead-in ...").
func parseLeadIn(s string) (time.Duration, error) {
	hour, minute, err := schedule.ParseHHMM(s)
	if err != nil {
		return 0, errors.New("must be HH:MM, e.g. 03:00 for three hours")
	}
	return time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute, nil
}

// leadInValidator adapts parseLeadIn to fyne.StringValidator.
func leadInValidator(s string) error {
	_, err := parseLeadIn(s)
	return err
}

// formatLeadIn renders a persisted offset back into the entry. Its output must
// always be something parseLeadIn accepts, or the field would seed itself into
// an invalid state on startup.
//
// Negatives are clamped rather than printed: schedule.Spec.Validate already
// forbids them, and "%02d" on a negative duration yields the nonsense
// "-1:-30". An offset of 24h or more is deliberately NOT clamped -- it is
// already invalid, and showing "24:00" with the field marked invalid tells the
// user the truth instead of silently rewriting their config.
func formatLeadIn(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Minute)
	return fmt.Sprintf("%02d:%02d", d/time.Hour, (d%time.Hour)/time.Minute)
}
