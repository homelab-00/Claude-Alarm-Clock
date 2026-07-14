package ui

import (
	"fmt"
	"strconv"
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
	runNowBtn *widget.Button
	buttons   *fyne.Container

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

	resultCard := NewCard(container.NewVScroll(w.result))
	resultCard.Resize(fyne.NewSize(0, 140))

	return container.NewPadded(container.NewVBox(
		clockCard,
		form,
		w.buttons,
		advanced,
		resultCard,
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

	w.runNowBtn.Hide()

	switch e.Status {
	case app.StatusIdle:
		w.status.SetText("Idle")
		w.setArmButton("Arm", widget.HighImportance)

	case app.StatusArmed:
		w.status.SetText(fmt.Sprintf("Armed · fires in %s (at %s, for a %s target)",
			roundDur(e.Remaining), e.FireAt.Format("15:04:05"), e.Target.Format("15:04")))
		w.setArmButton("Disarm", widget.DangerImportance)

	case app.StatusRunning:
		w.status.SetText("Running Claude Code…")
		w.setArmButton("Disarm", widget.DangerImportance)

	case app.StatusDone:
		w.status.SetText(fmt.Sprintf("Done · %s · $%.4f",
			roundDur(e.Result.Duration), e.Result.CostUSD))
		w.result.SetText(e.Result.Text)
		w.setArmButton("Arm", widget.HighImportance)

	case app.StatusMissed:
		w.status.SetText(fmt.Sprintf("MISSED · the alarm was due at %s, %s ago. Claude Code was not run.",
			e.FireAt.Format("15:04:05"), roundDur(e.Late)))
		w.setArmButton("Arm", widget.HighImportance)
		w.runNowBtn.Show()

	case app.StatusError:
		w.status.SetText("Error · " + e.Err.Error())
		w.setArmButton("Arm", widget.HighImportance)
	}

	// Hide/Show only refresh the button itself, not the Border container that
	// lays it out, so the row must be told to recompute -- otherwise Arm stays
	// sized as if Run now were still occupying its half of the row.
	w.buttons.Refresh()
}

func (w *Window) setArmButton(label string, imp widget.Importance) {
	w.armBtn.Text = label
	w.armBtn.Importance = imp
	w.armBtn.Refresh()
}

func (w *Window) onArm() {
	if w.armBtn.Text == "Disarm" {
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

	mins, err := strconv.Atoi(w.offsetEntry.Text)
	if err != nil {
		return config.State{}, fmt.Errorf("lead-in must be a whole number of minutes")
	}
	if mins < 0 {
		return config.State{}, fmt.Errorf("lead-in cannot be negative")
	}

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
