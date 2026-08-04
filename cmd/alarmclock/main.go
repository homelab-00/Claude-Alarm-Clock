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
	"runtime/debug"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"

	"claudealarm/internal/app"
	"claudealarm/internal/buildinfo"
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

// version is injected at link time by the release workflow:
//
//	go build -ldflags "-X main.version=$GITHUB_REF_NAME"
//
// It must remain an UNINITIALISED package-level string. The linker's -X is
// only effective on a string variable that is uninitialised or initialised to
// a constant expression; if this ever becomes a const, a struct field, or is
// initialised by a function call, -X silently does nothing and every release
// reports "dev".
//
// The symbol the linker looks for is literally "main.version". The module path
// (claudealarm) is not part of it.
var version string

func main() {
	showVersion := flag.Bool("version", false, "print version information and exit")
	hidden := flag.Bool("hidden", false, "start minimised to the tray")
	flag.Parse()

	// Before any Fyne initialisation. -version has to work with no display,
	// because the release workflow runs it as the smoke test that proves the
	// linker actually injected the tag.
	if *showVersion {
		bi, ok := debug.ReadBuildInfo()
		fmt.Println("Claude Alarm Clock " + buildinfo.String(buildinfo.Resolve(version, bi, ok)))
		return
	}

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
