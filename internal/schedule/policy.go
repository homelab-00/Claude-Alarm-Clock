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
// Round(0) strips the monotonic reading from BOTH operands before comparing
// them. Go's time.Time prefers the monotonic reading over the wall clock in
// Before() and Sub(), but only when BOTH operands carry one -- if either side
// has been stripped, wall-clock semantics already apply on their own. So this
// guard only changes behaviour in the case where both now and fireAt carry a
// monotonic reading, and a suspend or clock step has let it diverge from the
// wall clock. Comparing monotonic clocks in that case would silently ignore
// the suspend -- exactly the bug this whole package exists to prevent.
//
// Today that case cannot actually arise: fireAt is always produced by
// Spec.FireAt via time.Date (see spec.go), and time.Date never attaches a
// monotonic reading. So right now, Decide compares wall clocks whether or not
// Round(0) is here -- the guard is not currently load-bearing. It earns its
// place anyway, defensively, for a caller this package cannot foresee: pass a
// fireAt derived from time.Now() (e.g. time.Now().Add(10*time.Minute), which
// does carry a monotonic reading) alongside a now that also carries one, and
// without Round(0) a real suspend would go undetected here.
//
// This hazard is not unit-testable: producing a genuine wall/monotonic
// divergence requires an actual kernel suspend or clock step, which no
// in-process test can fabricate. It is verified instead by the real-suspend
// item in the manual test checklist. See TestDecideMonotonicAgreement in
// policy_test.go for what a unit test can and cannot establish here.
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
