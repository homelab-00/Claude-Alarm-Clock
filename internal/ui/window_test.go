package ui

import (
	"errors"
	"image/color"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

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

// wantFireTime computes the fire time/target the given form would produce,
// using the exact same source of truth (schedule.Spec.FireAt) that
// refreshHeroAndExplainer uses -- so these tests never hardcode a wall-clock
// answer that would drift as soon as they're run at a different time of day.
func wantFireTime(t *testing.T, hour, minute int, offset time.Duration) (fire, target time.Time) {
	t.Helper()
	fire, target, err := schedule.Spec{Hour: hour, Minute: minute, Offset: offset}.FireAt(time.Now())
	if err != nil {
		t.Fatalf("wantFireTime: %v", err)
	}
	return fire, target
}

func TestWindowRendersTheCurrentTimeQuietly(t *testing.T) {
	w := newTestWindow(t)

	w.SetClock(time.Date(2026, 7, 14, 7, 4, 5, 0, time.Local))

	if got := w.nowText.Text; got != "now 07:04:05" {
		t.Fatalf("nowText = %q, want \"now 07:04:05\"", got)
	}
}

// The hero readout is the app's biggest element; while idle (or running,
// done, missed, errored) it must show the fire time the current form would
// produce, not the current time.
func TestWindowHeroShowsFireTimeWhenIdle(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("07:30")
	w.offsetEntry.SetText("00:05")
	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	fire, _ := wantFireTime(t, 7, 30, 5*time.Minute)
	want := fire.Format("15:04")
	if w.hero.Text != want {
		t.Fatalf("hero = %q, want the form's fire time %q", w.hero.Text, want)
	}
}

// While armed, the hero readout is a live countdown to the fire time, not
// the fire time itself.
func TestWindowArmedStateShowsCountdown(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status:    app.StatusArmed,
		Now:       time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local),
		FireAt:    time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local),
		Target:    time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local),
		Remaining: 10 * time.Minute,
	})

	if w.hero.Text != "00:10:00" {
		t.Fatalf("hero = %q, want the countdown 00:10:00", w.hero.Text)
	}
	if w.armBtn.Text != "Disarm" {
		t.Fatalf("arm button = %q, want Disarm while armed", w.armBtn.Text)
	}
}

// The armed countdown must actually tick: a fresh StatusArmed event with a
// smaller Remaining must move the hero, since the alarm re-emits one every
// second while armed.
func TestWindowArmedCountdownTicksDownEachSecond(t *testing.T) {
	w := newTestWindow(t)

	base := app.Event{
		Status: app.StatusArmed,
		FireAt: time.Date(2026, 7, 14, 7, 10, 0, 0, time.Local),
		Target: time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local),
	}

	first := base
	first.Now = time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local)
	first.Remaining = 10 * time.Minute
	w.Apply(first)
	if w.hero.Text != "00:10:00" {
		t.Fatalf("hero = %q, want 00:10:00", w.hero.Text)
	}

	second := base
	second.Now = time.Date(2026, 7, 14, 7, 0, 1, 0, time.Local)
	second.Remaining = 9*time.Minute + 59*time.Second
	w.Apply(second)
	if w.hero.Text != "00:09:59" {
		t.Fatalf("hero = %q, want 00:09:59 after the next tick", w.hero.Text)
	}
}

// The explainer sentence is what actually teaches the difference between
// "target time" and "run this early": it must name the fire time, the
// lead-in, and the target together.
func TestWindowExplainerNamesFireTimeLeadInAndTarget(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("07:30")
	w.offsetEntry.SetText("00:05")
	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	fire, target := wantFireTime(t, 7, 30, 5*time.Minute)
	text := w.explainer.Text
	if !strings.Contains(text, fire.Format("15:04")) {
		t.Fatalf("explainer = %q, want it to name the fire time %s", text, fire.Format("15:04"))
	}
	if !strings.Contains(text, "5m before") {
		t.Fatalf("explainer = %q, want it to name the 5 minute lead-in", text)
	}
	if !strings.Contains(text, target.Format("15:04")) {
		t.Fatalf("explainer = %q, want it to name the target %s", text, target.Format("15:04"))
	}
}

// A zero lead-in is a real, valid configuration (fire exactly at the
// target); the explainer must say so in words rather than "0m before".
func TestWindowExplainerZeroLeadInSaysExactlyAtTarget(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("07:30")
	w.offsetEntry.SetText("00:00")
	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if !strings.Contains(w.explainer.Text, "exactly at your target") {
		t.Fatalf("explainer = %q, want it to say \"exactly at your target\" for a zero lead-in", w.explainer.Text)
	}
	if strings.Contains(w.explainer.Text, "0m before") {
		t.Fatalf("explainer = %q, must not say \"0m before\"", w.explainer.Text)
	}
}

// While armed, the explainer describes the schedule that was actually
// armed, without the "(in ...)" suffix -- the countdown above it already
// says how long from now.
func TestWindowExplainerWhileArmedNamesFireTimeAndTargetWithoutInSuffix(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status:    app.StatusArmed,
		Now:       time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local),
		FireAt:    time.Date(2026, 7, 14, 7, 25, 0, 0, time.Local),
		Target:    time.Date(2026, 7, 14, 7, 30, 0, 0, time.Local),
		Remaining: 25 * time.Minute,
	})

	text := w.explainer.Text
	if !strings.Contains(text, "07:25") || !strings.Contains(text, "5m before") || !strings.Contains(text, "07:30") {
		t.Fatalf("explainer = %q, want it to name fire time 07:25, the 5m lead-in, and the 07:30 target", text)
	}
	if strings.Contains(text, "(in ") {
		t.Fatalf("explainer = %q, must not repeat the \"(in ...)\" suffix while armed (the hero is already a live countdown)", text)
	}
}

// An invalid form (bad time, or bad lead-in) must not silently show a stale
// or wrong fire time -- the hero must show the dashes, and the explainer
// must surface the actual validation problem.
func TestWindowInvalidFormShowsDashesAndTheValidationProblem(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("7:30") // un-padded, rejected by schedule.ParseHHMM
	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if w.hero.Text != invalidFireTime {
		t.Fatalf("hero = %q, want %q for an invalid target time", w.hero.Text, invalidFireTime)
	}
	if !strings.Contains(w.explainer.Text, "Target time") {
		t.Fatalf("explainer = %q, want it to name the target-time problem", w.explainer.Text)
	}
}

// Same as above, but for an invalid lead-in specifically -- both fields must
// independently break the form.
func TestWindowInvalidLeadInShowsDashesAndTheValidationProblem(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("07:30")
	w.offsetEntry.SetText("abc")
	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if w.hero.Text != invalidFireTime {
		t.Fatalf("hero = %q, want %q for an invalid lead-in", w.hero.Text, invalidFireTime)
	}
	if !strings.Contains(w.explainer.Text, "Run this early") {
		t.Fatalf("explainer = %q, want it to name the lead-in problem", w.explainer.Text)
	}
}

// The explainer (and hero) must update live as the user types, without
// waiting for an Apply event -- OnChanged on both entries must be wired.
func TestWindowExplainerUpdatesLiveAsFormIsEdited(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("09:00")
	w.offsetEntry.SetText("00:10")
	afterFirstEdit := w.explainer.Text

	w.timeEntry.SetText("18:00")
	afterSecondEdit := w.explainer.Text

	if afterFirstEdit == afterSecondEdit {
		t.Fatalf("explainer did not change after editing the target time (still %q)", afterSecondEdit)
	}
	fire, _ := wantFireTime(t, 18, 0, 10*time.Minute)
	if !strings.Contains(afterSecondEdit, fire.Format("15:04")) {
		t.Fatalf("explainer = %q, want it to reflect the newly typed target time (fire at %s)", afterSecondEdit, fire.Format("15:04"))
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
	if w.resultCard.Hidden {
		t.Fatal("the result pane must be visible once there is an answer")
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

// The lead-in is entered as a duration on an HH:MM face, like the target time
// above it -- "03:00" is three hours. A bare minute count is exactly the
// spelling this replaced, so it must not quietly keep working: "180" would
// otherwise be read as 1 hour 80 minutes, or as three hours, depending on who
// is guessing.
func TestWindowOffsetEntryValidatesHHMM(t *testing.T) {
	w := newTestWindow(t)

	if err := w.offsetEntry.Validate(); err != nil {
		t.Fatalf("the default lead-in must be valid: %v", err)
	}

	for _, s := range []string{"03:00", "02:30", "00:00", "00:20", "23:59"} {
		w.offsetEntry.SetText(s)
		if err := w.offsetEntry.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want it to accept", s, err)
		}
	}

	for _, s := range []string{"abc", "", "180", "20", "3:00", "24:00", "00:60"} {
		w.offsetEntry.SetText(s)
		if err := w.offsetEntry.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want it to reject", s)
		}
	}
}

// The entry is seeded from the persisted offset, and what it shows must be a
// duration a human reads at a glance -- five minutes as "00:05", not "5".
func TestWindowOffsetEntrySeedsFromPersistedOffsetAsHHMM(t *testing.T) {
	w := newTestWindow(t)

	// newTestWindow's core carries config.DefaultState, whose lead-in is
	// config.DefaultOffset (5 minutes).
	if got := w.offsetEntry.Text; got != "00:05" {
		t.Fatalf("offsetEntry = %q, want the 5-minute default rendered as \"00:05\"", got)
	}
}

// stateFromForm is what Arm sends to the Core; it must reflect the widgets.
func TestWindowStateFromForm(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("08:45")
	w.offsetEntry.SetText("02:30")

	st, err := w.stateFromForm()
	if err != nil {
		t.Fatal(err)
	}

	if st.Spec.Hour != 8 || st.Spec.Minute != 45 {
		t.Fatalf("spec = %d:%d, want 8:45", st.Spec.Hour, st.Spec.Minute)
	}
	if st.Spec.Offset != 2*time.Hour+30*time.Minute {
		t.Fatalf("offset = %v, want 2h30m -- \"02:30\" is a duration, not 2 minutes 30 seconds", st.Spec.Offset)
	}
}

// Whatever the form produces must satisfy schedule.Spec.Validate, which bounds
// the lead-in to [0, 24h). Entering it on an HH:MM face makes that automatic
// (00:00-23:59 is exactly that range), and this pins it.
func TestWindowStateFromFormProducesAValidSpec(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("08:45")
	for _, s := range []string{"00:00", "00:05", "03:00", "23:59"} {
		w.offsetEntry.SetText(s)
		st, err := w.stateFromForm()
		if err != nil {
			t.Errorf("stateFromForm with lead-in %q: %v", s, err)
			continue
		}
		if err := st.Spec.Validate(); err != nil {
			t.Errorf("lead-in %q produced a Spec that fails Validate: %v", s, err)
		}
	}
}

// parseLeadIn must be the single source of truth for the offset field:
// stateFromForm used to re-derive the rules and omit the upper bound, so the
// widget marked "1500" invalid while Arm accepted it anyway -- the same "two
// validators disagree" bug class as the time field. Every input the widget
// rejects, Arm must reject too.
func TestWindowOffsetEntryValidatorMatchesStateFromForm(t *testing.T) {
	w := newTestWindow(t)
	w.timeEntry.SetText("07:30")

	for _, s := range []string{"1500", "180", "24:00", "3:00", "abc"} {
		w.offsetEntry.SetText(s)
		if err := w.offsetEntry.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want the widget to reject it", s)
		}
		if _, err := w.stateFromForm(); err == nil {
			t.Errorf("stateFromForm accepted lead-in %q that the widget rejects", s)
		}
	}
}

// A three-hour lead-in must read as three hours everywhere it appears. The
// field says "03:00"; the sentence beneath the clock says "3h", not the "180
// min" the user would otherwise have to divide in their head.
func TestWindowExplainerNamesTheLeadInInHoursAndMinutes(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("12:00")
	w.offsetEntry.SetText("03:00")
	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if !strings.Contains(w.explainer.Text, "3h before") {
		t.Fatalf("explainer = %q, want a 3-hour lead-in named as \"3h before\"", w.explainer.Text)
	}
	if strings.Contains(w.explainer.Text, "180") {
		t.Fatalf("explainer = %q, must not spell a 3-hour lead-in as a bare minute count", w.explainer.Text)
	}

	w.offsetEntry.SetText("02:30")
	if !strings.Contains(w.explainer.Text, "2h 30m before") {
		t.Fatalf("explainer = %q, want a 150-minute lead-in named as \"2h 30m before\"", w.explainer.Text)
	}
}

// The same wording rule applies to the settled ("Arm again to run at ...") and
// armed ("Runs at ...") sentences, which are separate format strings.
func TestWindowSettledAndArmedExplainersNameTheLeadInInHours(t *testing.T) {
	w := newTestWindow(t)

	w.timeEntry.SetText("12:00")
	w.offsetEntry.SetText("03:00")
	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Now(),
		Result: runner.Result{Text: "Hello!", CostUSD: 0.0044, Duration: 3 * time.Second},
	})
	if !strings.Contains(w.explainer.Text, "3h before") {
		t.Fatalf("settled explainer = %q, want the lead-in named as \"3h before\"", w.explainer.Text)
	}

	w.Apply(app.Event{
		Status:    app.StatusArmed,
		Now:       time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local),
		FireAt:    time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local),
		Target:    time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local),
		Remaining: 3 * time.Hour,
	})
	if !strings.Contains(w.explainer.Text, "3h before") {
		t.Fatalf("armed explainer = %q, want the lead-in named as \"3h before\"", w.explainer.Text)
	}
}

// The result pane is what displays Claude's answer -- the entire point of
// the app. It must stay out of the way (hidden) until there is actually an
// answer, so the window is compact at rest, and once shown it must never
// collapse to a one-line sliver: container.VBox lays out children at their
// MinSize, and Scroll.MinSize() defaults to max(32, s.minSize) rather than
// growing with content, so without an explicit floor the pane would render
// as a sliver no matter how long the answer is.
func TestWindowResultPaneIsHiddenAtRestAndDoesNotCollapseOnceShown(t *testing.T) {
	w := newTestWindow(t)

	if !w.resultCard.Hidden {
		t.Fatal("the result pane must be hidden before anything has run")
	}

	long := strings.Repeat("This is a long answer from Claude with many lines of text.\n", 15)
	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Date(2026, 7, 14, 7, 10, 4, 0, time.Local),
		Result: runner.Result{Text: long},
	})

	if w.resultCard.Hidden {
		t.Fatal("the result pane must become visible once there is an answer")
	}
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

// A ClockOnly tick must move the "now" line but must not overwrite whatever
// terminal status/result/hero/explainer text is currently on screen -- see
// app.Event.ClockOnly and the EventTick handling in internal/app/core.go.
// Before this fix, EventTick only ever produced a UI event when armed or
// exactly StatusIdle, so the clock froze solid after Done/Missed/Error and
// for the whole duration of a run.
func TestWindowClockOnlyTickDoesNotOverwriteDoneText(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Date(2026, 7, 14, 7, 10, 4, 0, time.Local),
		Result: runner.Result{Text: "Hello! How can I help you today?", CostUSD: 0.0044, Duration: 3312 * time.Millisecond},
	})
	wantStatus := w.status.Text
	wantResult := w.result.Text
	wantHero := w.hero.Text
	wantExplainer := w.explainer.Text

	w.Apply(app.Event{
		Status:    app.StatusDone,
		Now:       time.Date(2026, 7, 14, 7, 10, 5, 0, time.Local),
		ClockOnly: true,
	})

	if got := w.nowText.Text; got != "now 07:10:05" {
		t.Fatalf("nowText = %q, want \"now 07:10:05\" (a ClockOnly tick must still move it)", got)
	}
	if w.status.Text != wantStatus {
		t.Fatalf("status = %q, want unchanged %q (a ClockOnly tick must not touch the status text)", w.status.Text, wantStatus)
	}
	if w.result.Text != wantResult {
		t.Fatalf("result = %q, want unchanged %q (a ClockOnly tick must not touch the result text)", w.result.Text, wantResult)
	}
	if w.hero.Text != wantHero {
		t.Fatalf("hero = %q, want unchanged %q (a ClockOnly tick must not touch the hero readout)", w.hero.Text, wantHero)
	}
	if w.explainer.Text != wantExplainer {
		t.Fatalf("explainer = %q, want unchanged %q (a ClockOnly tick must not touch the explainer)", w.explainer.Text, wantExplainer)
	}
	if w.resultCard.Hidden {
		t.Fatal("a ClockOnly tick must not hide the result pane out from under a Done answer")
	}
}

// A stale answer from a previous run must not remain visible underneath a
// later non-Done status -- the user would read a success that did not
// happen. Clear the result pane when a new run starts, and on Error/Missed.
func TestWindowResultPaneClearsOnNewRunAndOnErrorAndMissed(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusDone, Now: time.Now(), Result: runner.Result{Text: "old answer"}})
	if w.result.Text == "" {
		t.Fatal("setup: StatusDone must populate the result pane")
	}
	w.Apply(app.Event{Status: app.StatusRunning, Now: time.Now()})
	if w.result.Text != "" {
		t.Fatalf("result = %q, want cleared when a new run starts", w.result.Text)
	}
	if !w.resultCard.Hidden {
		t.Fatal("the result pane must be hidden once cleared on StatusRunning")
	}

	w.Apply(app.Event{Status: app.StatusDone, Now: time.Now(), Result: runner.Result{Text: "old answer"}})
	w.Apply(app.Event{Status: app.StatusError, Now: time.Now(), Err: errors.New("boom")})
	if w.result.Text != "" {
		t.Fatalf("result = %q, want cleared on StatusError", w.result.Text)
	}
	if !w.resultCard.Hidden {
		t.Fatal("the result pane must be hidden once cleared on StatusError")
	}

	w.Apply(app.Event{Status: app.StatusDone, Now: time.Now(), Result: runner.Result{Text: "old answer"}})
	w.Apply(app.Event{Status: app.StatusMissed, Now: time.Now()})
	if w.result.Text != "" {
		t.Fatalf("result = %q, want cleared on StatusMissed", w.result.Text)
	}
	if !w.resultCard.Hidden {
		t.Fatal("the result pane must be hidden once cleared on StatusMissed")
	}
}

// Regression test for the Advanced accordion rendering empty when expanded:
// container.VBox resizes each child to that child's own MinSize regardless
// of the space actually available, so the accordion's detail fields were
// being squeezed to near nothing once opened in a window that wasn't
// oversized relative to the expanded content. build() wraps the whole body
// in a Scroll to fix this -- see the comment there; a Scroll always resizes
// its Content to at least Content.MinSize(), so the expanded fields get
// their real size and a scrollbar appears for whatever the window can't
// show at once. Pixel-level layout timing is exercised visually instead
// (see the screenshot capture in the design report), so this test pins the
// structural fix rather than exact widget sizes.
func TestWindowBodyIsWrappedInAScrollSoAccordionCanGrow(t *testing.T) {
	w := newTestWindow(t)

	if _, ok := w.content.(*container.Scroll); !ok {
		t.Fatalf("w.content is %T, want the whole body wrapped in a *container.Scroll -- "+
			"see the comment on build() explaining why the Advanced accordion needs it", w.content)
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

// inDuration backs the explainer's "(in ...)" suffix and must always keep a
// space between components, unlike humanDur.
func TestInDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{11*time.Hour + 35*time.Minute, "11h 35m"},
		{11 * time.Hour, "11h"},
		{35 * time.Minute, "35m"},
		{0, "0m"},
	}
	for _, c := range cases {
		if got := inDuration(c.in); got != c.want {
			t.Errorf("inDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// formatCountdown renders the armed hero as zero-padded HH:MM:SS.
func TestFormatCountdown(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{11*time.Hour + 34*time.Minute + 58*time.Second, "11:34:58"},
		{9 * time.Second, "00:00:09"},
		{0, "00:00:00"},
	}
	for _, c := range cases {
		if got := formatCountdown(c.in); got != c.want {
			t.Errorf("formatCountdown(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// After a run settles, the explainer must stop promising a FUTURE run. Reading
// "Claude Code will run at 07:25" directly beneath "Done · $0.0044" is
// confusing: it describes a hypothetical re-arm, not what just happened.
func TestWindowExplainerStopsPromisingAFutureRunOnceSettled(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})
	if !strings.Contains(w.explainer.Text, "will run") {
		t.Fatalf("idle explainer = %q, want it to promise a future run", w.explainer.Text)
	}

	w.Apply(app.Event{
		Status: app.StatusDone,
		Now:    time.Now(),
		Result: runner.Result{Text: "Hello!", CostUSD: 0.0044, Duration: 3 * time.Second},
	})

	if strings.Contains(w.explainer.Text, "will run") {
		t.Fatalf("explainer after Done = %q; it must not still promise a future run", w.explainer.Text)
	}
	if !strings.Contains(w.explainer.Text, "Arm again") {
		t.Fatalf("explainer after Done = %q, want it to invite a re-arm", w.explainer.Text)
	}

	// And it goes back to the future tense once re-armed.
	w.Apply(app.Event{
		Status: app.StatusArmed, Now: time.Now(),
		FireAt: time.Now().Add(time.Hour), Target: time.Now().Add(90 * time.Minute),
		Remaining: time.Hour,
	})
	if strings.Contains(w.explainer.Text, "Arm again") {
		t.Fatalf("explainer while armed = %q; the re-arm invitation must be gone", w.explainer.Text)
	}
}

// canvas.Text captures its colour when constructed. If the hero kept that
// stale colour, a window built before the theme was installed would render its
// largest element invisible -- the same trap the Card widget exists to avoid
// with canvas.Rectangle's FillColor. The hero must re-read the theme on every
// refresh.
func TestWindowHeroRereadsItsColourOnRefresh(t *testing.T) {
	w := newTestWindow(t)

	w.hero.Color = color.NRGBA{R: 1, G: 2, B: 3, A: 4} // a colour no theme returns

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	want := theme.Color(theme.ColorNameForeground)
	if w.hero.Color != want {
		t.Fatalf("hero colour = %v after refresh, want the theme's foreground %v; "+
			"a stale colour renders the app's largest element invisible", w.hero.Color, want)
	}
}
