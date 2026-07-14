package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

func newTestWindow(t *testing.T) *Window {
	t.Helper()
	test.NewApp()

	clk := schedule.NewTestClock(time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local))
	mt := schedule.NewManualTicker()
	al := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt })
	core := app.New(clk, al, &runner.Fake{}, config.NewMemStore(config.DefaultState(t.TempDir())))

	return NewWindow(core)
}

func TestWindowRendersAClock(t *testing.T) {
	w := newTestWindow(t)

	w.SetClock(time.Date(2026, 7, 14, 7, 4, 5, 0, time.Local))

	if got := w.clock.Text; got != "07:04:05" {
		t.Fatalf("clock = %q, want 07:04:05", got)
	}
}

func TestWindowArmedStateShowsCountdown(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status:    app.StatusArmed,
		Now:       time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local),
		FireAt:    time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local),
		Target:    time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local),
		Remaining: 10 * time.Minute,
	})

	if !strings.Contains(w.status.Text, "10m") {
		t.Fatalf("status = %q, want it to show the countdown", w.status.Text)
	}
	if strings.Contains(w.status.Text, "10m0s") {
		t.Fatalf("status = %q, want the trailing zero seconds dropped (\"10m\", not \"10m0s\")", w.status.Text)
	}
	if w.armBtn.Text != "Disarm" {
		t.Fatalf("arm button = %q, want Disarm while armed", w.armBtn.Text)
	}
}

func TestWindowDoneStateShowsTheAnswer(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Date(2026, 7, 14, 7, 10, 4, 0, time.Local),
		Result: runner.Result{
			Text:     "Hello! How can I help you today?",
			CostUSD:  0.0044,
			Duration: 3312 * time.Millisecond,
		},
	})

	if !strings.Contains(w.result.Text, "How can I help you") {
		t.Fatalf("result = %q, want the model's answer", w.result.Text)
	}
	if !strings.Contains(w.status.Text, "$0.0044") {
		t.Fatalf("status = %q, want the cost", w.status.Text)
	}
	if w.armBtn.Text != "Arm" {
		t.Fatalf("arm button = %q, want Arm after firing", w.armBtn.Text)
	}
}

func TestWindowMissedStateShowsHowLateAndOffersRunNow(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusMissed,
		Now:    time.Date(2026, 7, 14, 11, 20, 0, 0, time.Local),
		FireAt: time.Date(2026, 7, 14, 8, 45, 0, 0, time.Local),
		Late:   2*time.Hour + 35*time.Minute,
	})

	if !strings.Contains(strings.ToUpper(w.status.Text), "MISSED") {
		t.Fatalf("status = %q, want it to say MISSED", w.status.Text)
	}
	if !strings.Contains(w.status.Text, "2h35m") {
		t.Fatalf("status = %q, want it to say how late", w.status.Text)
	}
	if strings.Contains(w.status.Text, "2h35m0s") {
		t.Fatalf("status = %q, want the trailing zero seconds dropped (\"2h35m\", not \"2h35m0s\")", w.status.Text)
	}
	if w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be visible in the MISSED state")
	}
}

func TestWindowErrorStateShowsTheError(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusError,
		Now:    time.Date(2026, 7, 14, 7, 12, 0, 0, time.Local),
		Err:    errors.New("claude timed out after 2m0s"),
	})

	if !strings.Contains(w.status.Text, "timed out") {
		t.Fatalf("status = %q, want the error surfaced", w.status.Text)
	}
}

// Apply is a public method; nothing enforces that a StatusError event carries
// a non-nil Err. It must not panic on a nil-pointer dereference of e.Err.
func TestWindowErrorStateWithNilErrDoesNotPanic(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusError,
		Now:    time.Date(2026, 7, 14, 7, 12, 0, 0, time.Local),
		Err:    nil,
	})

	if !strings.Contains(w.status.Text, "Error") {
		t.Fatalf("status = %q, want a generic error surfaced instead of a panic", w.status.Text)
	}
}

// Run now is only for the MISSED state; it must not be visible otherwise.
func TestWindowRunNowIsHiddenUnlessMissed(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if !w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be hidden when not missed")
	}
}

// Unlike TestWindowRunNowIsHiddenUnlessMissed, which applies a single event to
// a freshly constructed Window (where Run now starts hidden regardless of
// what Apply does), this exercises the actual MISSED -> not-MISSED
// transition: it must re-hide Run now, not just leave it hidden from
// construction.
func TestWindowRunNowIsHiddenAfterLeavingMissed(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusMissed, Now: time.Now()})
	if w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be visible in the MISSED state")
	}

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})
	if !w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be hidden again after leaving MISSED")
	}
}

// The button row must not reserve space for a hidden Run now: a
// GridWithColumns would keep the cell even while hidden, leaving Arm stuck at
// half width in every state except MISSED.
func TestWindowArmButtonSpansFullWidthWhenRunNowHidden(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})
	if !w.runNowBtn.Hidden {
		t.Fatal("Run now must be hidden outside the MISSED state")
	}

	idleWidth := w.buttons.MinSize().Width
	armWidth := w.armBtn.MinSize().Width
	if idleWidth != armWidth {
		t.Fatalf("buttons row MinSize width = %v while Run now is hidden, want it to match Arm's own width %v "+
			"(the row must not reserve a cell for a hidden button)", idleWidth, armWidth)
	}

	w.Apply(app.Event{Status: app.StatusMissed, Now: time.Now()})
	if w.runNowBtn.Hidden {
		t.Fatal("Run now must be visible in the MISSED state")
	}

	missedWidth := w.buttons.MinSize().Width
	if missedWidth <= idleWidth {
		t.Fatalf("buttons row MinSize width = %v while Run now is shown, want it wider than the hidden-state width %v",
			missedWidth, idleWidth)
	}
}

func TestWindowTimeEntryValidatesHHMM(t *testing.T) {
	w := newTestWindow(t)

	if err := w.timeEntry.Validate(); err != nil {
		t.Fatalf("the default time must be valid: %v", err)
	}

	w.timeEntry.SetText("25:99")
	if err := w.timeEntry.Validate(); err == nil {
		t.Fatal("25:99 must not validate")
	}

	w.timeEntry.SetText("07:30")
	if err := w.timeEntry.Validate(); err != nil {
		t.Fatalf("07:30 must validate: %v", err)
	}
}

// The target-time entry's validator must be exactly as strict as
// stateFromForm's parser (schedule.ParseHHMM), or the field can tell the user
// their input is valid and then Arm can reject it. In particular, Go's "15"
// layout verb is variable-width, so time.Parse("15:04", "7:30") succeeds --
// an un-padded validator would wrongly accept "7:30".
func TestWindowTimeEntryValidatorMatchesParseHHMM(t *testing.T) {
	w := newTestWindow(t)

	accept := []string{"07:30", "00:00", "23:59"}
	for _, s := range accept {
		w.timeEntry.SetText(s)
		if err := w.timeEntry.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want it to accept", s, err)
		}
	}

	reject := []string{"7:30", "24:00", "07:60", "abc", ""}
	for _, s := range reject {
		w.timeEntry.SetText(s)
		if err := w.timeEntry.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want it to reject (un-padded input must not pass; "+
				"stateFromForm's schedule.ParseHHMM rejects it, and the two must agree)", s)
		}
	}
}

func TestWindowOffsetEntryRejectsNonNumeric(t *testing.T) {
	w := newTestWindow(t)

	w.offsetEntry.SetText("abc")
	if err := w.offsetEntry.Validate(); err == nil {
		t.Fatal("a non-numeric offset must not validate")
	}

	w.offsetEntry.SetText("20")
	if err := w.offsetEntry.Validate(); err != nil {
		t.Fatalf("20 must validate: %v", err)
	}
}

// stateFromForm is what Arm sends to the Core; it must reflect the widgets.
func TestWindowStateFromForm(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("08:45")
	w.offsetEntry.SetText("15")

	st, err := w.stateFromForm()
	if err != nil {
		t.Fatal(err)
	}

	if st.Spec.Hour != 8 || st.Spec.Minute != 45 {
		t.Fatalf("spec = %d:%d, want 8:45", st.Spec.Hour, st.Spec.Minute)
	}
	if st.Spec.Offset != 15*time.Minute {
		t.Fatalf("offset = %v, want 15m", st.Spec.Offset)
	}
}

// minutesValidator's upper bound (n >= 24*60) must be the single source of
// truth for the offset field: stateFromForm used to re-derive the check and
// omit that bound, so the widget marked "1500" invalid while Arm accepted it
// anyway -- the same "two validators disagree" bug class as the time field.
func TestWindowOffsetEntryUpperBoundMatchesStateFromForm(t *testing.T) {
	w := newTestWindow(t)

	w.offsetEntry.SetText("1500")
	if err := w.offsetEntry.Validate(); err == nil {
		t.Fatal("1500 minutes (>= 24h) must not validate")
	}

	_, err := w.stateFromForm()
	if err == nil {
		t.Fatal("stateFromForm must reject a 1500-minute lead-in, matching the widget's own validator")
	}
}

// The result pane is what displays Claude's answer -- the entire point of the
// app. container.VBox lays out its children at their MinSize, and
// Scroll.MinSize() defaults to max(32, s.minSize) rather than growing with
// content, so without an explicit floor the pane renders as a one-line
// sliver no matter how long the answer is.
func TestWindowResultPaneDoesNotCollapse(t *testing.T) {
	w := newTestWindow(t)

	if got := w.resultCard.MinSize().Height; got < 140 {
		t.Fatalf("result pane MinSize height = %v while empty, want at least 140", got)
	}

	long := strings.Repeat("This is a long answer from Claude with many lines of text.\n", 15)
	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Date(2026, 7, 14, 7, 10, 4, 0, time.Local),
		Result: runner.Result{Text: long},
	})

	if got := w.resultCard.MinSize().Height; got < 140 {
		t.Fatalf("result pane MinSize height = %v after a long answer, want at least 140 (the pane must not collapse)", got)
	}
}

// The Arm/Disarm behaviour must be driven by Window.armed, not by comparing
// armBtn.Text: renaming or localising that label (this app's owner works in
// Greece) must not silently break arming/disarming.
func TestWindowArmDisarmIsNotKeyedToButtonLabel(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusArmed, Now: time.Now(), FireAt: time.Now().Add(time.Minute), Target: time.Now().Add(time.Minute)})
	if !w.armed {
		t.Fatal("armed must be true after a StatusArmed event")
	}

	// Simulate localisation: the label no longer says "Disarm", but the
	// button must still behave as the disarm control.
	w.armBtn.Text = "Απενεργοποίηση"

	w.onArm()

	if w.core.State().Armed {
		t.Fatal("onArm must have called Disarm even though the button's label was not literally \"Disarm\"")
	}
}

// humanDur must drop Duration.String()'s trailing zero-valued components
// ("10m0s" -> "10m", "2h35m0s" -> "2h35m", "1h0m0s" -> "1h") while leaving
// genuinely sub-minute or non-zero readings alone.
func TestHumanDur(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Minute, "10m"},
		{2*time.Hour + 35*time.Minute, "2h35m"},
		{time.Hour, "1h"},
		{45 * time.Second, "45s"},
		{5*time.Minute + 30*time.Second, "5m30s"},
	}
	for _, c := range cases {
		if got := humanDur(c.in); got != c.want {
			t.Errorf("humanDur(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
