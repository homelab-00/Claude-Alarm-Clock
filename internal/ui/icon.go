package ui

import (
	"fyne.io/fyne/v2"

	"claudealarm/assets"
)

// Icon is the app icon: used for both the tray icon and the window icon.
//
// It is a plain fyne.StaticResource, never a theme.ThemedResource --
// ThemedResource routes to SetTemplateIcon, a macOS-only path that renders as
// an empty icon on Linux.
//
// The //go:embed directive itself lives in assets/icon.go, not here: Go's
// embed patterns may not contain ".." to reach outside the declaring file's
// own directory, so a file in internal/ui cannot embed assets/icon.png
// directly. See that file for the embed, and internal/ui/fonts.go for the
// same pattern applied to the vendored font.
//
// Regenerate the PNG with: go run ./tools/genicon
var Icon fyne.Resource = fyne.NewStaticResource("icon.png", assets.IconPNG)
