package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/schedule"
)

// invalidFireTime is the hero text shown when the form cannot produce a fire
// time at all (bad HH:MM, bad lead-in).
const invalidFireTime = "--:--"

// Window is the main view.
//
// Every method that touches a widget must run on the Fyne goroutine. Apply is
// called only from bridge.go, inside fyne.Do.
type Window struct {
	core *app.Core
	win  fyne.Window // set by Attach; nil in tests

	// hero is the app's one big number: a live countdown to the fire time
	// while armed, or the fire time the current form would produce
	// otherwise. This deliberately replaced a clock showing the current
	// time -- the user already has a clock in their desktop panel; the
	// only thing this app uniquely knows is when Claude will run.
	hero *canvas.Text
	// explainer spells out, in a sentence, what hero's number means: the
	// fire time, the lead-in, and the target it precedes. It is what
	// actually teaches the user the difference between the two fields.
	explainer *widget.Label
	status    *widget.Label
	result    *widget.Label

	// nowText is the current time, demoted to a small, quiet line: it is
	// context (and the app's only "I am alive" tray signal), not content.
	nowText *canvas.Text

	timeEntry   *widget.Entry
	offsetEntry *widget.Entry
	workDirEnt  *widget.Entry
	modelEntry  *widget.Entry
	promptEntry *widget.Entry

	armBtn *widget.Button
	armed  bool // drives onArm's branch; armBtn.Text is presentational only

	// settled is true once a run has finished one way or another (Done, Missed
	// or Error). It only changes the explainer's wording: a settled window
	// describes a hypothetical re-arm ("Arm again to run at 07:25") rather than
	// promising a future run ("Claude Code will run at 07:25"), which reads as
	// confused sitting directly under "Done · $0.0044".
	settled bool

	runNowBtn *widget.Button
	buttons   *fyne.Container
	advanced  *widget.Accordion

	// armedFireAt/armedTarget/armedRemaining cache the schedule from the
	// most recent StatusArmed event (the alarm re-emits one every second
	// while armed, ticking Remaining down). hero/explainer read these,
	// not the live form: editing the fields while armed must not
	// retroactively change what has already been scheduled.
	armedFireAt    time.Time
	armedTarget    time.Time
	armedRemaining time.Duration

	resultCard *Card // shows Claude's answer; its MinSize must never collapse

	content fyne.CanvasObject
}

// NewWindow builds the view. Call Content() for the object to put in a window.
func NewWindow(core *app.Core) *Window {
	w := &Window{core: core}
	st := core.State()

	w.hero = canvas.NewText(invalidFireTime, theme.Color(theme.ColorNameForeground))
	w.hero.TextSize = theme.Size(SizeNameClock)
	w.hero.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	w.hero.Alignment = fyne.TextAlignCenter

	w.explainer = widget.NewLabel("")
	w.explainer.Alignment = fyne.TextAlignCenter
	w.explainer.Wrapping = fyne.TextWrapWord

	w.status = widget.NewLabel("Idle")
	w.status.Alignment = fyne.TextAlignCenter
	w.status.Wrapping = fyne.TextWrapWord

	w.result = widget.NewLabel("")
	w.result.Wrapping = fyne.TextWrapWord

	// nowText is quiet on purpose -- see the doc comment on the Window
	// field. It still exists purely so the app has a visible heartbeat
	// while it sits in the tray.
	w.nowText = canvas.NewText("now --:--:--", theme.Color(theme.ColorNamePlaceHolder))
	w.nowText.TextSize = theme.Size(theme.SizeNameCaptionText)
	w.nowText.Alignment = fyne.TextAlignCenter

	w.timeEntry = widget.NewEntry()
	w.timeEntry.SetPlaceHolder("HH:MM")
	w.timeEntry.SetText(fmt.Sprintf("%02d:%02d", st.Spec.Hour, st.Spec.Minute))
	// Fyne has no time picker, and neither does fyne-x. An alarm is a typing
	// interaction anyway: you type 07:30 and press enter.
	//
	// This must be schedule.ParseHHMM, not validation.NewTime("15:04"): Go's
	// "15" verb is variable-width, so time.Parse alone accepts "7:30". If the
	// entry's validator were looser than ParseHHMM, the field would show
	// "7:30" as valid and then Arm would reject it -- two validators
	// disagreeing about the same input.
	w.timeEntry.Validator = timeValidator

	w.offsetEntry = widget.NewEntry()
	w.offsetEntry.SetPlaceHolder("minutes")
	w.offsetEntry.SetText(strconv.Itoa(int(st.Spec.Offset / time.Minute)))
	w.offsetEntry.Validator = minutesValidator

	// The hero/explainer must track the form live, as the user types --
	// this sentence is the only place the two fields' relationship is
	// spelled out in plain language.
	w.timeEntry.OnChanged = func(string) { w.refreshHeroAndExplainer() }
	w.offsetEntry.OnChanged = func(string) { w.refreshHeroAndExplainer() }

	w.workDirEnt = widget.NewEntry()
	w.workDirEnt.SetText(st.WorkDir)

	w.modelEntry = widget.NewEntry()
	w.modelEntry.SetText(st.Model)

	w.promptEntry = widget.NewEntry()
	w.promptEntry.SetText(st.Prompt)

	w.armBtn = widget.NewButton("Arm", w.onArm)
	w.armBtn.Importance = widget.HighImportance

	w.runNowBtn = widget.NewButton("Run now", func() { w.core.RunNow() })
	w.runNowBtn.Hide() // only shown in the MISSED state

	w.content = w.build()
	// Render the initial hero/explainer from the form's starting values,
	// same as any later edit would.
	w.refreshHeroAndExplainer()
	return w
}

// Attach gives the Window a real fyne.Window, so it can raise dialogs. Not set
// in tests.
func (w *Window) Attach(win fyne.Window) { w.win = win }

// Content is the root object.
func (w *Window) Content() fyne.CanvasObject { return w.content }

func (w *Window) build() fyne.CanvasObject {
	heroCard := NewCard(container.NewVBox(
		layoutCentre(w.hero),
		w.explainer,
		w.status,
	))

	form := widget.NewForm(
		widget.NewFormItem("Target time", w.timeEntry),
		widget.NewFormItem("Run this early", w.offsetEntry),
	)

	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), w.onBrowse)
	w.advanced = widget.NewAccordion(
		widget.NewAccordionItem("Advanced", widget.NewForm(
			widget.NewFormItem("Working dir", container.NewBorder(nil, nil, nil, browse, w.workDirEnt)),
			widget.NewFormItem("Model", w.modelEntry),
			widget.NewFormItem("Prompt", w.promptEntry),
		)),
	)

	// Run now is hidden except in StatusMissed. A GridWithColumns would keep
	// reserving its cell even while hidden, leaving Arm stuck at half width;
	// Border only allocates space to runNowBtn while it is Visible(), so Arm
	// fills the row whenever Run now is not shown. Apply refreshes this
	// container whenever runNowBtn's visibility changes, so the layout is
	// recomputed rather than left stale from construction.
	w.buttons = container.NewBorder(nil, nil, nil, w.runNowBtn, w.armBtn)

	// container.VBox lays out children at their MinSize and discards any
	// pre-layout Resize() call, and Scroll.MinSize() is max(32, s.minSize) --
	// it does not grow with content. SetMinSize is the real API for giving a
	// scroll region a floor height, so the result pane (the whole point of
	// this app) never collapses to a one-line sliver.
	resultScroll := container.NewVScroll(w.result)
	resultScroll.SetMinSize(fyne.NewSize(0, 140))
	w.resultCard = NewCard(resultScroll)
	// Hidden at rest -- see Apply. The window should be compact until
	// there is actually an answer to show, not carry a permanent empty box.
	w.resultCard.Hide()

	// The window has a fixed size (see cmd/alarmclock/main.go), but the
	// Advanced accordion can grow the content's real MinSize well past it
	// when opened. container.VBox always resizes each child to that
	// child's own MinSize regardless of the space actually available (see
	// the comment above), so without a Scroll here the accordion's detail
	// fields get laid out into space the fixed window never grants them --
	// squeezed to near nothing, which reads as "renders empty" when
	// expanded. Wrapping the whole body in a Scroll fixes it the same way
	// resultScroll does above: Scroll.Refresh always resizes its Content to
	// at least Content.MinSize(), so the accordion's expanded fields get
	// their real size and a scrollbar appears for whatever the fixed
	// window can't show at once.
	return container.NewVScroll(container.NewPadded(container.NewVBox(
		heroCard,
		form,
		w.buttons,
		w.advanced,
		w.resultCard,
		w.nowText,
	)))
}

func layoutCentre(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewCenter(o)
}

// SetClock updates the small, quiet "now" line. Fyne goroutine only.
//
// This used to drive the 88pt hero display; it was demoted because the
// current time is context the user already has on their desktop clock, not
// content only this app knows. It still has to tick every second regardless
// of state -- it is the app's only visible "I am alive" signal while it sits
// in the tray.
func (w *Window) SetClock(t time.Time) {
	w.nowText.Text = "now " + t.Format("15:04:05")
	w.nowText.Refresh()
}

// Apply renders one Event. Fyne goroutine only -- bridge.go wraps every call in
// fyne.Do.
func (w *Window) Apply(e app.Event) {
	if !e.Now.IsZero() {
		w.SetClock(e.Now)
	}

	// A ClockOnly event carries nothing but Now: the "now" line above has
	// already been moved, and there is nothing else to do. In particular
	// this must return before the runNowBtn.Hide()/switch below, or a bare
	// tick that lands while the app is resting on a terminal status
	// (Done/Missed/Error) would re-render that status from the tick's
	// zeroed-out fields -- wiping a real answer or error off the screen
	// with "Done · 0s · $0.0000" or similar. See app.Event.ClockOnly.
	if e.ClockOnly {
		return
	}

	w.runNowBtn.Hide()

	// A run that has finished one way or another changes only the explainer's
	// wording -- see the settled field. setArmButton calls
	// refreshHeroAndExplainer, so this must be set before the switch runs.
	w.settled = e.Status == app.StatusDone ||
		e.Status == app.StatusMissed ||
		e.Status == app.StatusError

	switch e.Status {
	case app.StatusIdle:
		w.status.SetText("Idle")
		w.setArmButton(false)

	case app.StatusArmed:
		// Cache this tick's schedule for hero/explainer -- see the doc
		// comment on the armedFireAt/armedTarget/armedRemaining fields.
		w.armedFireAt = e.FireAt
		w.armedTarget = e.Target
		w.armedRemaining = e.Remaining
		w.status.SetText("Armed")
		w.setArmButton(true)

	case app.StatusRunning:
		w.status.SetText("Running Claude Code…")
		// A new run starts: clear out whatever answer the result pane was
		// showing from the previous run. Otherwise, if this run errors, a
		// stale success stays on screen underneath the "Error ·" status and
		// the user reads a success that did not happen.
		w.result.SetText("")
		w.resultCard.Hide()
		w.setArmButton(true)

	case app.StatusDone:
		w.status.SetText(fmt.Sprintf("Done · %s · $%.4f",
			humanDur(roundDur(e.Result.Duration)), e.Result.CostUSD))
		w.result.SetText(e.Result.Text)
		w.resultCard.Show()
		w.setArmButton(false)

	case app.StatusMissed:
		w.status.SetText(fmt.Sprintf("MISSED · the alarm was due at %s, %s ago. Claude Code was not run.",
			e.FireAt.Format("15:04:05"), humanDur(roundDur(e.Late))))
		w.result.SetText("")
		w.resultCard.Hide()
		w.setArmButton(false)
		w.runNowBtn.Show()

	case app.StatusError:
		msg := "unknown error"
		if e.Err != nil {
			msg = e.Err.Error()
		}
		w.status.SetText("Error · " + msg)
		w.result.SetText("")
		w.resultCard.Hide()
		w.setArmButton(false)

	default:
		// app.Status is a closed enum (internal/app/core.go); every value it
		// defines is handled above. If a new one is ever added without a
		// matching case here, say so loudly instead of silently leaving
		// stale text on screen.
		w.status.SetText(fmt.Sprintf("Unknown status: %v", e.Status))
		w.setArmButton(false)
	}

	// setArmButton above has updated w.armed for this event, so the hero and
	// explainer branch on the right bucket: the live countdown while armed,
	// or the fire time the current form would produce otherwise.
	w.refreshHeroAndExplainer()

	// Hide/Show only refresh the button itself, not the Border container that
	// lays it out, so the row must be told to recompute -- otherwise Arm stays
	// sized as if Run now were still occupying its half of the row.
	w.buttons.Refresh()
}

// setArmButton sets both the armed/disarmed state that onArm branches on and
// the button's presentation. The label is display-only -- renaming or
// localising it (this app's owner works in Greece) must never change
// behaviour, so onArm reads w.armed, never w.armBtn.Text.
func (w *Window) setArmButton(armed bool) {
	w.armed = armed
	if armed {
		w.armBtn.Text = "Disarm"
		w.armBtn.Importance = widget.DangerImportance
	} else {
		w.armBtn.Text = "Arm"
		w.armBtn.Importance = widget.HighImportance
	}
	w.armBtn.Refresh()
}

// refreshHeroAndExplainer recomputes the hero readout and the explainer
// sentence beneath it. It is the single place that decides what those two
// widgets say, and it is called from three places that all need them kept
// in sync: Apply (after every non-ClockOnly event), and the time/offset
// entries' OnChanged handlers (so the sentence updates live as the user
// types).
func (w *Window) refreshHeroAndExplainer() {
	// canvas.Text captures its colour when constructed, so re-read it on every
	// refresh. Without this the hero renders in whatever colour was current at
	// NewWindow time -- and if the theme is installed after the window is built
	// (reverse two lines in main.go and it is), that colour is the wrong one, so
	// the app's largest element silently renders invisible. Same trap the Card
	// widget exists to avoid with canvas.Rectangle's FillColor.
	w.hero.Color = theme.Color(theme.ColorNameForeground)

	if w.armed {
		w.hero.Text = formatCountdown(w.armedRemaining)
		w.explainer.SetText(armedExplainer(w.armedFireAt, w.armedTarget))
		w.hero.Refresh()
		return
	}

	fire, target, offset, err := w.formFireTime()
	if err != nil {
		w.hero.Text = invalidFireTime
		w.explainer.SetText(err.Error())
		w.hero.Refresh()
		return
	}

	w.hero.Text = fire.Format("15:04")

	// After a run has finished (or been missed, or failed), "Claude Code WILL
	// run at 07:25" sitting directly under "Done · $0.0044" reads as confused --
	// it is describing a hypothetical re-arm, not what just happened. Say so.
	if w.settled {
		w.explainer.SetText(rearmExplainer(fire, target, offset))
	} else {
		w.explainer.SetText(idleExplainer(fire, target, offset))
	}
	w.hero.Refresh()
}

// formFireTime computes the fire time the form would currently produce, using
// schedule.Spec.FireAt as the single source of truth for the arithmetic (it
// is DST-correct; this method must never re-derive it). Errors carry
// wording meant for the explainer sentence, not for a log -- the same
// friendly voice as the rest of the form.
func (w *Window) formFireTime() (fire, target time.Time, offset time.Duration, err error) {
	hour, minute, perr := schedule.ParseHHMM(w.timeEntry.Text)
	if perr != nil {
		return time.Time{}, time.Time{}, 0, errors.New("Target time must be HH:MM, e.g. 07:30")
	}

	// minutesValidator is the single source of truth for the offset field --
	// see the identical reasoning in stateFromForm.
	if verr := minutesValidator(w.offsetEntry.Text); verr != nil {
		return time.Time{}, time.Time{}, 0, fmt.Errorf("Run this early %s", verr)
	}
	mins, _ := strconv.Atoi(w.offsetEntry.Text) // minutesValidator already confirmed this parses
	offset = time.Duration(mins) * time.Minute

	spec := schedule.Spec{Hour: hour, Minute: minute, Offset: offset}
	fire, target, ferr := spec.FireAt(time.Now())
	if ferr != nil {
		return time.Time{}, time.Time{}, 0, ferr
	}
	return fire, target, offset, nil
}

// idleExplainer is the plain-language sentence for every state except
// Armed: it names the fire time the form would produce, the lead-in, the
// target, and how long from now that fire time is -- the "(in 11h 35m)"
// suffix disambiguates a fire time that has rolled over to tomorrow
// morning (see schedule.Spec.NextTarget).
func idleExplainer(fire, target time.Time, offset time.Duration) string {
	in := inDuration(time.Until(fire))
	if offset <= 0 {
		return fmt.Sprintf("Claude Code will run at %s — exactly at your target (in %s)",
			fire.Format("15:04"), in)
	}
	return fmt.Sprintf("Claude Code will run at %s — %d min before your %s target (in %s)",
		fire.Format("15:04"), int(offset/time.Minute), target.Format("15:04"), in)
}

// rearmExplainer is the sentence shown once a run has settled (Done, Missed, or
// Error). idleExplainer's future tense -- "Claude Code WILL run at 07:25" --
// reads as confused directly beneath "Done · $0.0044", because it is describing
// a hypothetical re-arm rather than the run that just happened. This says which.
func rearmExplainer(fire, target time.Time, offset time.Duration) string {
	if offset <= 0 {
		return fmt.Sprintf("Arm again to run at %s, exactly at your target.",
			fire.Format("15:04"))
	}
	return fmt.Sprintf("Arm again to run at %s — %d min before your %s target.",
		fire.Format("15:04"), int(offset/time.Minute), target.Format("15:04"))
}

// armedExplainer is the plain-language sentence while Armed. Unlike
// idleExplainer it has no "(in ...)" suffix -- the hero readout right above
// it is already a live countdown, so repeating "how long from now" would be
// redundant.
func armedExplainer(fireAt, target time.Time) string {
	offset := target.Sub(fireAt)
	if offset <= 0 {
		return fmt.Sprintf("Runs at %s — exactly at your target", fireAt.Format("15:04"))
	}
	return fmt.Sprintf("Runs at %s — %d min before your %s target",
		fireAt.Format("15:04"), int(offset/time.Minute), target.Format("15:04"))
}

// inDuration formats a duration for the explainer's "(in ...)" suffix, e.g.
// "11h 35m" or "45m". Unlike humanDur (used for the status line's "ago"/
// "fires in" phrasing elsewhere), this always keeps a space between the
// hour and minute components, matching the sentence it sits inside.
func inDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Minute)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// formatCountdown renders the armed hero readout as HH:MM:SS, ticking every
// second as Remaining counts down.
func formatCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func (w *Window) onArm() {
	if w.armed {
		w.core.Disarm()
		return
	}

	st, err := w.stateFromForm()
	if err != nil {
		w.fail(err)
		return
	}
	if err := w.core.Arm(st); err != nil {
		w.fail(err)
	}
}

func (w *Window) onBrowse() {
	if w.win == nil {
		return
	}
	dialog.ShowFolderOpen(func(list fyne.ListableURI, err error) {
		if err != nil || list == nil {
			return
		}
		w.workDirEnt.SetText(list.Path())
	}, w.win)
}

// stateFromForm reads the widgets into a config.State.
func (w *Window) stateFromForm() (config.State, error) {
	st := w.core.State()

	hour, minute, err := schedule.ParseHHMM(w.timeEntry.Text)
	if err != nil {
		return config.State{}, err
	}

	// minutesValidator is the single source of truth for the offset field, for
	// the same reason timeValidator is for the time field above: if
	// stateFromForm re-derived its own rules and they drifted from the
	// widget's validator (as they did before this fix -- the widget enforced
	// an upper bound of 24h that this method did not), the field could show
	// "valid" for input Arm then rejects, with two different error messages.
	if err := minutesValidator(w.offsetEntry.Text); err != nil {
		return config.State{}, fmt.Errorf("lead-in %s", err)
	}
	mins, _ := strconv.Atoi(w.offsetEntry.Text) // minutesValidator already confirmed this parses

	st.Spec.Hour = hour
	st.Spec.Minute = minute
	st.Spec.Offset = time.Duration(mins) * time.Minute
	st.WorkDir = w.workDirEnt.Text
	st.Model = w.modelEntry.Text
	st.Prompt = w.promptEntry.Text

	return st, nil
}

func (w *Window) fail(err error) {
	w.status.SetText("Error · " + err.Error())
	if w.win != nil {
		dialog.ShowError(err, w.win)
	}
}

// timeValidator is the single source of truth for the target-time entry: it
// is the exact same parser stateFromForm uses to arm the alarm, so the field
// never shows "valid" for input that Arm will then reject.
func timeValidator(s string) error {
	_, _, err := schedule.ParseHHMM(s)
	return err
}

func minutesValidator(s string) error {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("must be a whole number of minutes")
	}
	if n < 0 {
		return fmt.Errorf("cannot be negative")
	}
	if n >= 24*60 {
		return fmt.Errorf("must be less than 24 hours")
	}
	return nil
}

// roundDur trims a duration to something a human wants to read.
func roundDur(d time.Duration) time.Duration {
	switch {
	case d >= time.Hour:
		return d.Round(time.Minute)
	case d >= time.Minute:
		return d.Round(time.Second)
	default:
		return d.Round(10 * time.Millisecond)
	}
}

// humanDur formats a duration for a human, not a debugger: Go's
// Duration.String() always prints down to seconds once minutes are present
// (10m0s) and down to minutes once hours are present (2h35m0s), so the UI
// read "fires in 10m0s" and "2h35m0s ago". This drops that trailing
// zero-valued component. roundDur has already rounded d to the coarsest unit
// that matters (minute once >= 1h, second once >= 1m), so h>0 implies the
// seconds component is always zero -- there is nothing to lose by omitting it.
// Below a minute, Go's own formatting is already precise and free of the bug
// (fractional seconds are trimmed of trailing zeros), so it is used as-is.
func humanDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	if d < time.Minute {
		return d.String()
	}

	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	s := (d % time.Minute) / time.Second

	var b strings.Builder
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	if s > 0 && h == 0 {
		fmt.Fprintf(&b, "%ds", s)
	}
	if b.Len() == 0 {
		return "0m"
	}
	return b.String()
}
