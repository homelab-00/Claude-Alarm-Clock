package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"claudealarm/internal/app"
)

// InstallTray registers the system tray icon and menu. It reports whether a
// tray was actually installed.
//
// The desktop.App assertion is not optional: Fyne's test app does not implement
// it, and neither does the mobile driver. Without the guard, this file could not
// be compiled into a test binary.
//
// Linux caveat: fyne.io/systray speaks StatusNotifierItem over D-Bus. If no SNI
// host is running, registration SILENTLY no-ops -- no error, no panic, just an
// invisible icon. Verified present on the target machine (KDE Plasma owns
// org.kde.StatusNotifierWatcher), which is why there is no fallback path here.
// On GNOME this would need the AppIndicator extension.
func InstallTray(a fyne.App, w fyne.Window, core *app.Core, icon fyne.Resource) bool {
	desk, ok := a.(desktop.App)
	if !ok {
		return false
	}

	quit := fyne.NewMenuItem("Quit", func() { a.Quit() })
	// Without IsQuit, Fyne appends its OWN Quit item, which calls App.Quit()
	// directly and skips anything we wanted to do first.
	quit.IsQuit = true

	menu := fyne.NewMenu("Claude Alarm",
		fyne.NewMenuItem("Show", func() {
			w.Show()
			w.RequestFocus()
		}),
		fyne.NewMenuItem("Disarm", func() {
			if core != nil {
				core.Disarm()
			}
		}),
		fyne.NewMenuItemSeparator(),
		quit,
	)

	desk.SetSystemTrayMenu(menu)
	if icon != nil {
		// Must be a plain full-colour PNG. A theme.ThemedResource routes to
		// SetTemplateIcon, which is a macOS-only path and useless on Linux.
		desk.SetSystemTrayIcon(icon)
	}
	desk.SetSystemTrayWindow(w)

	return true
}

// closableWindow is the slice of fyne.Window that KeepAliveOnClose needs.
//
// Narrowed deliberately: fyne.Window is a large interface with no way to read
// the close intercept back, so taking it whole would leave the single most
// load-bearing line in the app impossible to test. fyne.Window satisfies this.
type closableWindow interface {
	SetCloseIntercept(func())
	Hide()
	Close()
}

// KeepAliveOnClose makes the window's close button hide it instead of
// destroying it.
//
// This single line is what keeps the process -- and therefore the alarm --
// alive. Fyne's destroyWindow() ends with `if len(d.windows) == 0 { d.Quit() }`
// and has NO system-tray exception, so without this, clicking X kills the app
// even with a tray icon registered.
//
// Corollary: never call w.Close() anywhere in app code. Close bypasses the
// intercept entirely.
func KeepAliveOnClose(w closableWindow) {
	w.SetCloseIntercept(func() { w.Hide() })
}
