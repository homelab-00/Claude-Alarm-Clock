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

// Run now is only for the MISSED state; it must not be visible otherwise.
func TestWindowRunNowIsHiddenUnlessMissed(t *testing.T) {
	w := newTestWindow(t)

	w.Apply(app.Event{Status: app.StatusIdle, Now: time.Now()})

	if !w.runNowBtn.Hidden {
		t.Fatal("the Run now button must be hidden when not missed")
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
