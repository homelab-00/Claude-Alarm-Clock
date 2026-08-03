package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

// Card exists precisely because a bare canvas.Rectangle samples its FillColor
// once, at construction, and never re-colours when the theme changes -- a
// widget with a renderer re-reads the theme on every Refresh instead. These
// tests exercise the renderer directly, so a regression back to a bare
// Rectangle (or a renderer that caches its colour) would fail here.

// withCardTheme installs th as the app's current theme for the duration of
// the test, then restores the stock test theme so later tests are unaffected.
func withCardTheme(t *testing.T, th fyne.Theme) {
	t.Helper()
	test.ApplyTheme(t, th)
	t.Cleanup(func() { test.ApplyTheme(t, test.NewTheme()) })
}

// newCardRenderer builds a Card over content and returns its concrete
// renderer, failing the test if CreateRenderer stops returning a
// *cardRenderer (e.g. if Card were reverted to a bare canvas.Rectangle).
func newCardRenderer(t *testing.T, content fyne.CanvasObject) (*Card, *cardRenderer) {
	t.Helper()
	c := NewCard(content)
	r, ok := c.CreateRenderer().(*cardRenderer)
	if !ok {
		t.Fatalf("CreateRenderer() = %T, want *cardRenderer", c.CreateRenderer())
	}
	t.Cleanup(r.Destroy)
	return c, r
}

func TestCardRendererBackgroundMatchesThemeCardColor(t *testing.T) {
	withCardTheme(t, NewTheme())

	_, r := newCardRenderer(t, canvas.NewRectangle(color.Transparent))

	if got, want := r.bg.FillColor, colCard; got != want {
		t.Fatalf("bg.FillColor = %v, want the theme's card colour %v", got, want)
	}
}

// fakeCardTheme lets a test change what ColorNameCard resolves to, so it can
// prove Refresh re-reads the theme rather than keeping the colour sampled at
// construction -- the entire reason Card exists instead of a bare
// canvas.Rectangle.
type fakeCardTheme struct {
	fyne.Theme
	card color.Color
}

func (f fakeCardTheme) Color(n fyne.ThemeColorName, v fyne.ThemeVariant) color.Color {
	if n == ColorNameCard {
		return f.card
	}
	return f.Theme.Color(n, v)
}

func TestCardRendererRereadsThemeOnRefresh(t *testing.T) {
	withCardTheme(t, NewTheme())

	_, r := newCardRenderer(t, canvas.NewRectangle(color.Transparent))
	original := r.bg.FillColor

	swapped := color.NRGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xFF}
	test.ApplyTheme(t, fakeCardTheme{Theme: NewTheme(), card: swapped})

	r.Refresh()

	if r.bg.FillColor == original {
		t.Fatal("bg.FillColor did not change after Refresh with a different theme; " +
			"Card must re-read the theme on Refresh, not cache the colour from construction")
	}
	if r.bg.FillColor != swapped {
		t.Fatalf("bg.FillColor after Refresh = %v, want the new theme's card colour %v", r.bg.FillColor, swapped)
	}
}

func TestCardMinSizeAccountsForContentAndPadding(t *testing.T) {
	withCardTheme(t, NewTheme())

	content := canvas.NewRectangle(color.Transparent)
	content.SetMinSize(fyne.NewSize(100, 50))

	c, r := newCardRenderer(t, content)

	pad := theme.SizeForWidget(theme.SizeNamePadding, c) * 2
	want := fyne.NewSize(100+2*pad, 50+2*pad)

	if got := r.MinSize(); got != want {
		t.Fatalf("MinSize() = %v, want %v (content plus padding); "+
			"a wrong MinSize silently collapses the card", got, want)
	}
}

func TestCardObjectsIncludesBackgroundAndContent(t *testing.T) {
	withCardTheme(t, NewTheme())

	content := canvas.NewRectangle(color.Transparent)
	_, r := newCardRenderer(t, content)

	objs := r.Objects()
	if len(objs) != 2 {
		t.Fatalf("Objects() has %d items, want 2 (background + content)", len(objs))
	}

	var hasBg, hasContent bool
	for _, o := range objs {
		if o == fyne.CanvasObject(r.bg) {
			hasBg = true
		}
		if o == content {
			hasContent = true
		}
	}
	if !hasBg {
		t.Fatal("Objects() does not include the background rectangle")
	}
	if !hasContent {
		t.Fatal("Objects() does not include the content")
	}
}
