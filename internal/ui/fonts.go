package ui

import (
	"fyne.io/fyne/v2"

	"claudealarm/assets/fonts"
)

// JetBrains Mono Bold, SIL OFL 1.1. See assets/fonts/OFL.txt.
//
// Chosen for tabular digits: every glyph is the same width, so the clock face
// does not shift sideways as the seconds tick over.
//
// The //go:embed directive itself lives in assets/fonts/fonts.go, not here:
// Go's embed patterns may not contain ".." to reach outside the declaring
// file's own directory, so a file in internal/ui cannot embed a file under
// assets/fonts/ directly. See that file for the embed.

// resMonoBold is the embedded clock font, or nil if it was not vendored -- in
// which case the theme falls back to Fyne's built-in monospace.
var resMonoBold = func() fyne.Resource {
	if len(fonts.MonoBoldTTF) == 0 {
		return nil
	}
	return fyne.NewStaticResource("JetBrainsMono-Bold.ttf", fonts.MonoBoldTTF)
}()
