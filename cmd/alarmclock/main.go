// Command alarmclock schedules a Claude Code run for a chosen time.
//
// The user enters a target time. The app fires at target minus a configurable
// lead-in, running the Claude Code CLI headlessly in a chosen directory. It
// lives in the system tray and keeps running when the window is closed.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
	"claudealarm/internal/ui"
)

// tickPeriod is how often we compare the wall clock against the fire time.
//
// One second costs about 0.007% of a core (measured) and bounds post-resume
// lateness to a second, while giving the clock face a free tick. It must be a
// period fed to a real time.Ticker (wall-clock polling), never a time.Timer
// computed once from a target duration -- a Timer is driven by the monotonic
// clock and loses time across a suspend.
const tickPeriod = time.Second

func main() {
	hidden := flag.Bool("hidden", false, "start minimised to the tray")
	flag.Parse()

	a := fyneapp.NewWithID("gr.polaris.claudealarm") // the ID is required for Preferences()
	a.SetIcon(ui.Icon)
	a.Settings().SetTheme(ui.NewTheme())

	store := config.NewPrefsStore(a.Preferences())
	clk := schedule.NewRealClock()
	alarm := schedule.NewAlarm(clk, tickPeriod, schedule.RealTicker)
	core := app.New(clk, alarm, runner.NewCLI(), store)

	win := ui.NewWindow(core)

	w := a.NewWindow("Claude Alarm Clock")
	w.SetContent(win.Content())
	w.Resize(fyne.NewSize(460, 620))
	win.Attach(w)

	// THE line that keeps the process alive when the user clicks X.
	ui.KeepAliveOnClose(w)

	if !ui.InstallTray(a, w, core, ui.Icon) {
		// No tray means the window is the only way back in, so never start
		// hidden -- the user would have no way to reach the app at all.
		log.Println("warning: no system tray available; the window is the only way to reach the app")
		*hidden = false
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go alarm.Run(ctx)
	go core.Run(ctx)
	go ui.Bridge(ctx, core, win)

	// Preflight: fail loudly now, while someone is looking, rather than at 07:10
	// when nobody is.
	if err := preflight(core); err != nil {
		log.Printf("preflight: %v", err)
	}

	// Re-arm anything that survived a restart. Restore honours the grace window,
	// because a shut-down app is unobserved time.
	if err := core.Restore(); err != nil {
		log.Printf("restore: %v", err)
	}

	a.Lifecycle().SetOnStopped(cancel)

	if *hidden {
		// NewWindow already registered the window, so the app stays alive
		// without ever showing it.
		a.Run()
	} else {
		w.ShowAndRun()
	}
}

// preflight checks, at startup, the things that would otherwise only fail at
// fire time: that claude exists, and that the working directory is real.
func preflight(core *app.Core) error {
	bin, err := runner.Lookup()
	if err != nil {
		return err
	}

	st := core.State()
	if err := st.ValidateWorkDir(); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "claude: %s\nworkdir: %s\ndefaults: %s\n",
		bin, st.WorkDir, runner.DefaultsSummary())
	return nil
}
