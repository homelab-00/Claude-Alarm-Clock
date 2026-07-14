package ui

import (
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

// Window is the main view.
//
// Every method that touches a widget must run on the Fyne goroutine. Apply is
// called only from bridge.go, inside fyne.Do.
type Window struct {
	core *app.Core
	win  fyne.Window // set by Attach; nil in tests

	clock  *canvas.Text
	status *widget.Label
	result *widget.Label

	timeEntry   *widget.Entry
	offsetEntry *widget.Entry
	workDirEnt  *widget.Entry
	modelEntry  *widget.Entry
	promptEntry *widget.Entry

	armBtn    *widget.Button
	armed     bool // drives onArm's branch; armBtn.Text is presentational only
	runNowBtn *widget.Button
	buttons   *fyne.Container

	resultCard *Card // shows Claude's answer; its MinSize must never collapse

	content fyne.CanvasObject
}

// NewWindow builds the view. Call Content() for the object to put in a window.
func NewWindow(core *app.Core) *Window {
	w := &Window{core: core}
	st := core.State()

	w.clock = canvas.NewText("--:--:--", theme.Color(theme.ColorNameForeground))
	w.clock.TextSize = theme.Size(SizeNameClock)
	w.clock.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	w.clock.Alignment = fyne.TextAlignCenter

	w.status = widget.NewLabel("Idle")
	w.status.Alignment = fyne.TextAlignCenter
	w.status.Wrapping = fyne.TextWrapWord

	w.result = widget.NewLabel("")
	w.result.Wrapping = fyne.TextWrapWord

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
	return w
}

// Attach gives the Window a real fyne.Window, so it can raise dialogs. Not set
// in tests.
func (w *Window) Attach(win fyne.Window) { w.win = win }

// Content is the root object.
func (w *Window) Content() fyne.CanvasObject { return w.content }

func (w *Window) build() fyne.CanvasObject {
	clockCard := NewCard(container.NewVBox(
		layoutCentre(w.clock),
		w.status,
	))

	form := widget.NewForm(
		widget.NewFormItem("Target time", w.timeEntry),
		widget.NewFormItem("Lead-in (min)", w.offsetEntry),
	)

	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), w.onBrowse)
	advanced := widget.NewAccordion(
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

	return container.NewPadded(container.NewVBox(
		clockCard,
		form,
		w.buttons,
		advanced,
		w.resultCard,
	))
}

func layoutCentre(o fyne.CanvasObject) fyne.CanvasObject {
	return container.NewCenter(o)
}

// SetClock updates the clock face. Fyne goroutine only.
func (w *Window) SetClock(t time.Time) {
	w.clock.Text = t.Format("15:04:05")
	w.clock.Refresh()
}

// Apply renders one Event. Fyne goroutine only -- bridge.go wraps every call in
// fyne.Do.
func (w *Window) Apply(e app.Event) {
	if !e.Now.IsZero() {
		w.SetClock(e.Now)
	}

	// A ClockOnly event carries nothing but Now: the clock above has already
	// been moved, and there is nothing else to do. In particular this must
	// return before the runNowBtn.Hide()/switch below, or a bare tick that
	// lands while the app is resting on a terminal status (Done/Missed/Error)
	// would re-render that status from the tick's zeroed-out fields --
	// wiping a real answer or error off the screen with "Done · 0s · $0.0000"
	// or similar. See app.Event.ClockOnly.
	if e.ClockOnly {
		return
	}

	w.runNowBtn.Hide()

	switch e.Status {
	case app.StatusIdle:
		w.status.SetText("Idle")
		w.setArmButton(false)

	case app.StatusArmed:
		w.status.SetText(fmt.Sprintf("Armed · fires in %s (at %s, for a %s target)",
			humanDur(roundDur(e.Remaining)), e.FireAt.Format("15:04:05"), e.Target.Format("15:04")))
		w.setArmButton(true)

	case app.StatusRunning:
		w.status.SetText("Running Claude Code…")
		// A new run starts: clear out whatever answer the result pane was
		// showing from the previous run. Otherwise, if this run errors, a
		// stale success stays on screen underneath the "Error ·" status and
		// the user reads a success that did not happen.
		w.result.SetText("")
		w.setArmButton(true)

	case app.StatusDone:
		w.status.SetText(fmt.Sprintf("Done · %s · $%.4f",
			humanDur(roundDur(e.Result.Duration)), e.Result.CostUSD))
		w.result.SetText(e.Result.Text)
		w.setArmButton(false)

	case app.StatusMissed:
		w.status.SetText(fmt.Sprintf("MISSED · the alarm was due at %s, %s ago. Claude Code was not run.",
			e.FireAt.Format("15:04:05"), humanDur(roundDur(e.Late))))
		w.result.SetText("")
		w.setArmButton(false)
		w.runNowBtn.Show()

	case app.StatusError:
		msg := "unknown error"
		if e.Err != nil {
			msg = e.Err.Error()
		}
		w.status.SetText("Error · " + msg)
		w.result.SetText("")
		w.setArmButton(false)

	default:
		// app.Status is a closed enum (internal/app/core.go); every value it
		// defines is handled above. If a new one is ever added without a
		// matching case here, say so loudly instead of silently leaving
		// stale text on screen.
		w.status.SetText(fmt.Sprintf("Unknown status: %v", e.Status))
		w.setArmButton(false)
	}

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
