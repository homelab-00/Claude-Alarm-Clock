package schedule

import "time"

// Decision is what a single tick concludes about an armed alarm.
type Decision int

const (
	// DecideWait: the fire time is still ahead.
	DecideWait Decision = iota
	// DecideFire: the fire time has arrived, or passed within the grace window.
	DecideFire
	// DecideMissed: the fire time passed longer ago than the grace window
	// allows. The machine was almost certainly suspended. Do not run.
	DecideMissed
)

func (d Decision) String() string {
	switch d {
	case DecideWait:
		return "wait"
	case DecideFire:
		return "fire"
	case DecideMissed:
		return "missed"
	default:
		return "unknown"
	}
}

// Decide is the whole firing policy, as a pure function.
//
// Round(0) strips the monotonic reading from BOTH operands. This is not
// cosmetic. When two time.Time values both carry a monotonic reading, Before()
// and Sub() silently use it in preference to the wall clock -- so without these
// two calls we would be comparing monotonic clocks inside a function whose
// entire purpose is to compare wall clocks, and the suspend bug would be back.
//
// The grace window governs UNOBSERVED time only: it answers "the app was not
// watching -- is this alarm now too stale to honour?" It is deliberately not
// consulted when the user arms an alarm explicitly (see Alarm.Arm).
func Decide(now, fireAt time.Time, grace time.Duration) Decision {
	now = now.Round(0)
	fireAt = fireAt.Round(0)

	if now.Before(fireAt) {
		return DecideWait
	}
	if now.Sub(fireAt) > grace {
		return DecideMissed
	}
	return DecideFire
}

// DetectJump reports whether the wall clock moved independently of the
// monotonic clock between two ticks, which means the machine was suspended or
// the clock was stepped (NTP, manual change, timezone change).
//
// The two clocks normally advance together. When they diverge by more than a
// couple of tick periods, something moved the wall clock behind our back, and
// every derived fire time must be recomputed.
func DetectJump(wallDelta, monoDelta, period time.Duration) (jump time.Duration, jumped bool) {
	jump = wallDelta - monoDelta

	tolerance := 2 * period
	if jump > tolerance || jump < -tolerance {
		return jump, true
	}
	return 0, false
}
