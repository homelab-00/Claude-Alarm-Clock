package ui

import (
	"testing"

	fynetest "fyne.io/fyne/v2/test"
)

// TestMain sets a Fyne test app as the current app before any test runs.
//
// theme.DefaultTheme().Color() calls fyne.CurrentApp().Settings().PrimaryColor()
// unconditionally (fyne v2.8.0, theme/theme.go), even for color names that
// don't need it. Without a current app that call panics, so every test in
// this package that touches theme.DefaultTheme() needs one set up first.
func TestMain(m *testing.M) {
	fynetest.NewApp()
	m.Run()
}
