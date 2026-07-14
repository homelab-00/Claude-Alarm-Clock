package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// A custom theme MUST fall through to the default for every name it does not
// override. A theme that returns zero values for unknown names renders the app
// as invisible text on an invisible background.
func TestThemeFallsThroughForUnhandledNames(t *testing.T) {
	th := NewTheme()

	got := th.Color(theme.ColorNamePrimary, theme.VariantDark)
	want := theme.DefaultTheme().Color(theme.ColorNamePrimary, theme.VariantDark)

	if got != want {
		t.Fatalf("Color(primary) = %v, want the default %v", got, want)
	}
}

func TestThemeOverridesBackgroundAndCard(t *testing.T) {
	th := NewTheme()

	bg := th.Color(theme.ColorNameBackground, theme.VariantDark)
	if bg == theme.DefaultTheme().Color(theme.ColorNameBackground, theme.VariantDark) {
		t.Fatal("background was not overridden")
	}
	if _, ok := bg.(color.NRGBA); !ok {
		t.Fatalf("background = %T, want color.NRGBA", bg)
	}

	card := th.Color(ColorNameCard, theme.VariantDark)
	if card == nil {
		t.Fatal("the card colour is not defined")
	}
	if card == bg {
		t.Fatal("the card must be visually distinct from the background")
	}
}

// The clock face is routed by TextStyle.Monospace. Symbol must be left alone --
// hijacking it breaks icon and emoji rendering.
func TestThemeFontRoutesMonospaceButNotSymbol(t *testing.T) {
	th := NewTheme()

	mono := th.Font(fyne.TextStyle{Monospace: true})
	if mono == nil {
		t.Fatal("Font(monospace) returned nil")
	}

	symbol := th.Font(fyne.TextStyle{Symbol: true})
	want := theme.DefaultTheme().Font(fyne.TextStyle{Symbol: true})
	if symbol != want {
		t.Fatal("Font(symbol) was hijacked; that breaks icon rendering")
	}

	regular := th.Font(fyne.TextStyle{})
	if regular != theme.DefaultTheme().Font(fyne.TextStyle{}) {
		t.Fatal("Font(regular) should fall through to the default")
	}
}

func TestThemeDefinesAClockSize(t *testing.T) {
	th := NewTheme()

	if got := th.Size(SizeNameClock); got < 48 {
		t.Fatalf("Size(clock) = %v, want something large enough to be a clock face", got)
	}
	// And unknown sizes fall through.
	if got, want := th.Size(theme.SizeNamePadding), theme.DefaultTheme().Size(theme.SizeNamePadding); got != want {
		t.Fatalf("Size(padding) = %v, want the default %v", got, want)
	}
}

func TestThemeRoundsCorners(t *testing.T) {
	th := NewTheme()

	if got := th.Size(theme.SizeNameInputRadius); got <= 0 {
		t.Fatalf("Size(inputRadius) = %v, want a positive radius", got)
	}
}
