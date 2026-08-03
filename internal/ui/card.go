package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Card is an elevated rounded panel.
//
// It is a widget rather than a bare canvas.Rectangle because a Rectangle
// samples its FillColor once, at construction, and will not re-colour when the
// theme variant changes. A renderer re-reads the theme on every Refresh.
type Card struct {
	widget.BaseWidget
	Content fyne.CanvasObject
}

// NewCard wraps content in an elevated panel.
func NewCard(content fyne.CanvasObject) *Card {
	c := &Card{Content: content}
	c.ExtendBaseWidget(c)
	return c
}

func (c *Card) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(theme.ColorForWidget(ColorNameCard, c))
	bg.CornerRadius = theme.SizeForWidget(theme.SizeNameCardRadius, c)

	return &cardRenderer{card: c, bg: bg}
}

type cardRenderer struct {
	card *Card
	bg   *canvas.Rectangle
}

func (r *cardRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)

	pad := theme.SizeForWidget(theme.SizeNamePadding, r.card) * 2
	r.card.Content.Move(fyne.NewPos(pad, pad))
	r.card.Content.Resize(fyne.NewSize(size.Width-2*pad, size.Height-2*pad))
}

func (r *cardRenderer) MinSize() fyne.Size {
	pad := theme.SizeForWidget(theme.SizeNamePadding, r.card) * 2
	m := r.card.Content.MinSize()
	return fyne.NewSize(m.Width+2*pad, m.Height+2*pad)
}

func (r *cardRenderer) Refresh() {
	// Re-read the theme, so a light/dark switch actually re-colours the card.
	r.bg.FillColor = theme.ColorForWidget(ColorNameCard, r.card)
	r.bg.CornerRadius = theme.SizeForWidget(theme.SizeNameCardRadius, r.card)
	r.bg.Refresh()
	canvas.Refresh(r.card)
}

func (r *cardRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.bg, r.card.Content}
}

func (r *cardRenderer) Destroy() {}
