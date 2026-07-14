package ui

import (
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"claudealarm/internal/app"
	"claudealarm/internal/config"
	"claudealarm/internal/runner"
	"claudealarm/internal/schedule"
)

// test.NewApp() does NOT implement desktop.App. That is exactly why InstallTray
// must guard the type assertion -- and this test proves the guard holds, so
// tray.go is safe to compile into a headless test binary.
func TestInstallTrayIsANoOpWithoutADesktopApp(t *testing.T) {
	a := test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close() // a test window; the real one is never Close()d

	installed := InstallTray(a, w, nil, nil)

	if installed {
		t.Fatal("InstallTray reported success on a non-desktop App")
	}
}

// fakeWindow records what KeepAliveOnClose does to a window.
//
// KeepAliveOnClose takes the narrow closableWindow interface rather than
// fyne.Window precisely so this is possible: fyne.Window is a large interface
// with no way to read the close intercept back, which would leave the single
// most load-bearing line in the app untestable.
type fakeWindow struct {
	intercept func()
	hidden    bool
	closed    bool
}

func (f *fakeWindow) SetCloseIntercept(fn func()) { f.intercept = fn }
func (f *fakeWindow) Hide()                       { f.hidden = true }
func (f *fakeWindow) Close()                      { f.closed = true }

// The close intercept is the single thing keeping the process alive when the
// user clicks X: Fyne's destroyWindow() quits the app when the last window
// closes, with NO system-tray exception. This test invokes the registered
// intercept and asserts it hides rather than closes.
func TestKeepAliveOnCloseHidesRatherThanClosing(t *testing.T) {
	f := &fakeWindow{}

	KeepAliveOnClose(f)

	if f.intercept == nil {
		t.Fatal("KeepAliveOnClose registered no close intercept; clicking X would kill the app")
	}

	f.intercept() // the user clicks X

	if !f.hidden {
		t.Fatal("the close intercept must Hide the window")
	}
	if f.closed {
		t.Fatal("the close intercept must NOT Close the window: Close destroys it, and Fyne quits when the last window is destroyed")
	}
}

// fyne.Window must still satisfy the narrow interface, or main.go will not
// compile.
func TestFyneWindowSatisfiesClosableWindow(t *testing.T) {
	test.NewApp()
	w := test.NewWindow(nil)
	defer w.Close()

	var _ closableWindow = w
}

// fakeTrayHost records what installTrayOn hands it, so the menu, icon, and
// window it builds can be inspected without a real desktop driver.
//
// installTrayOn takes the narrow trayHost interface rather than desktop.App
// precisely so this is possible: Fyne's test app does not implement
// desktop.App, which would otherwise leave the menu construction --
// including the load-bearing quit.IsQuit = true -- untestable.
type fakeTrayHost struct {
	menu *fyne.Menu
	icon fyne.Resource
	win  fyne.Window
}

func (h *fakeTrayHost) SetSystemTrayMenu(m *fyne.Menu)    { h.menu = m }
func (h *fakeTrayHost) SetSystemTrayIcon(r fyne.Resource) { h.icon = r }
func (h *fakeTrayHost) SetSystemTrayWindow(w fyne.Window) { h.win = w }

// fakeTrayHost must itself satisfy trayHost, or the tests below would not
// compile against installTrayOn.
var _ trayHost = (*fakeTrayHost)(nil)

func findMenuItem(t *testing.T, m *fyne.Menu, label string) *fyne.MenuItem {
	t.Helper()
	for _, it := range m.Items {
		if it.Label == label {
			return it
		}
	}
	t.Fatalf("no menu item labelled %q in %+v", label, m.Items)
	return nil
}

// The menu's shape and order matter: Show and Disarm must be reachable from
// the tray, separated visually from the destructive Quit action.
func TestInstallTrayOnBuildsTheExpectedMenu(t *testing.T) {
	host := &fakeTrayHost{}
	w := test.NewWindow(nil)
	defer w.Close()

	installTrayOn(host, w, nil, nil)

	if host.menu == nil {
		t.Fatal("SetSystemTrayMenu was never called")
	}

	items := host.menu.Items
	if len(items) != 4 {
		t.Fatalf("menu has %d items, want 4 (Show, Disarm, separator, Quit): %+v", len(items), items)
	}
	if items[0].Label != "Show" {
		t.Fatalf("item 0 = %q, want Show", items[0].Label)
	}
	if items[1].Label != "Disarm" {
		t.Fatalf("item 1 = %q, want Disarm", items[1].Label)
	}
	if !items[2].IsSeparator {
		t.Fatalf("item 2 = %+v, want a separator", items[2])
	}
	if items[3].Label != "Quit" {
		t.Fatalf("item 3 = %q, want Quit", items[3].Label)
	}
}

// This is the single most important assertion in this file. Without
// IsQuit = true, Fyne appends its OWN Quit item to the tray menu, and that
// item calls App.Quit() directly -- bypassing KeepAliveOnClose's
// hide-not-close intercept and any teardown InstallTray's own Quit item is
// meant to run first. The bug this test exists to catch is completely
// silent: the tray still shows a "Quit" label either way, so nothing short
// of this assertion distinguishes "our Quit item" from "Fyne's own".
func TestInstallTrayOnSetsIsQuitOnTheQuitItem(t *testing.T) {
	host := &fakeTrayHost{}
	w := test.NewWindow(nil)
	defer w.Close()

	installTrayOn(host, w, nil, nil)

	quit := findMenuItem(t, host.menu, "Quit")
	if !quit.IsQuit {
		t.Fatal("Quit.IsQuit must be true: without it, Fyne appends its own Quit item, " +
			"which calls App.Quit() directly and skips our teardown entirely")
	}
}

// The icon handed to SetSystemTrayIcon must be exactly the resource passed
// in. A theme.ThemedResource would route through SetTemplateIcon instead (a
// macOS-only path, useless on this Linux target), so installTrayOn must
// forward whatever plain, full-colour PNG resource it was given, unwrapped.
func TestInstallTrayOnPassesTheIconThroughUnchanged(t *testing.T) {
	host := &fakeTrayHost{}
	w := test.NewWindow(nil)
	defer w.Close()

	icon := fyne.NewStaticResource("icon.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n'})

	installTrayOn(host, w, nil, icon)

	if host.icon != icon {
		t.Fatalf("SetSystemTrayIcon received %v, want the exact resource passed in", host.icon)
	}
}

// SetSystemTrayWindow must receive the same window InstallTray was given, so
// the tray can show/hide/focus the right window.
func TestInstallTrayOnSetsTheTrayWindow(t *testing.T) {
	host := &fakeTrayHost{}
	w := test.NewWindow(nil)
	defer w.Close()

	installTrayOn(host, w, nil, nil)

	if host.win != w {
		t.Fatalf("SetSystemTrayWindow received %v, want the window passed in", host.win)
	}
}

// Tapping Disarm in the tray menu must reach the real Core.Disarm, not just
// exist as a label. A real Core is built with fakes (TestClock,
// ManualTicker, runner.Fake, MemStore) rather than a mock of Core itself, so
// this also proves installTrayOn's Disarm closure calls the actual method,
// not a stand-in with the same name.
func TestInstallTrayOnDisarmItemCallsCoreDisarm(t *testing.T) {
	clk := schedule.NewTestClock(time.Date(2026, 7, 14, 7, 0, 0, 0, time.Local))
	mt := schedule.NewManualTicker()
	al := schedule.NewAlarm(clk, time.Second, func(time.Duration) schedule.Ticker { return mt })
	core := app.New(clk, al, &runner.Fake{}, config.NewMemStore(config.DefaultState(t.TempDir())))

	// Target 07:30, 20m lead-in -> fires at 07:10, still ten minutes off, so
	// Arm leaves the core armed and counting down rather than firing inline.
	st := core.State()
	st.Spec = schedule.Spec{Hour: 7, Minute: 30, Offset: 20 * time.Minute, Grace: 5 * time.Minute}
	if err := core.Arm(st); err != nil {
		t.Fatalf("setup: Arm() error = %v", err)
	}
	if !core.State().Armed {
		t.Fatal("setup: core must be armed before this test can prove Disarm un-arms it")
	}

	host := &fakeTrayHost{}
	w := test.NewWindow(nil)
	defer w.Close()

	installTrayOn(host, w, core, nil)

	disarm := findMenuItem(t, host.menu, "Disarm")
	disarm.Action()

	if core.State().Armed {
		t.Fatal("tapping the Disarm menu item must call core.Disarm(), but the core is still armed")
	}
}
