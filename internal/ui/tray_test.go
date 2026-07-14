package ui

import (
	"testing"

	"fyne.io/fyne/v2/test"
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
