// Command genicon renders assets/icon.png, the 64x64 system tray icon.
// Run from the repo root: go run ./tools/genicon
package main

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

const (
	size = 64
	ss   = 8 // supersample factor, for antialiasing
)

var accent = color.NRGBA{R: 0x4C, G: 0x8D, B: 0xFF, A: 0xFF}

// onHand reports whether the point (dx,dy), relative to the clock centre, lies
// on a hand pointing at the given angle with the given length and half-width.
func onHand(dx, dy, angle, length, halfW float64) bool {
	ux, uy := math.Cos(angle), math.Sin(angle)
	along := dx*ux + dy*uy             // distance along the hand's axis
	across := math.Abs(-dx*uy + dy*ux) // perpendicular distance from it
	return along >= 0 && along <= length && across <= halfW
}

func main() {
	const big = size * ss
	centre := float64(big) / 2
	rOuter, rInner := float64(big)*0.46, float64(big)*0.36

	// coverage[y][x] counts how many supersamples landed on the glyph.
	coverage := make([]int, size*size)
	for y := 0; y < big; y++ {
		for x := 0; x < big; x++ {
			dx, dy := float64(x)+0.5-centre, float64(y)+0.5-centre
			d := math.Hypot(dx, dy)
			on := d <= rOuter && d >= rInner // the bezel ring
			if !on {
				on = onHand(dx, dy, -math.Pi/2, rInner*0.62, float64(ss)*2.2) || // hour hand, up
					onHand(dx, dy, 0, rInner*0.86, float64(ss)*1.6) // minute hand, right
			}
			if on {
				coverage[(y/ss)*size+(x/ss)]++
			}
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i, n := range coverage {
		c := accent
		c.A = uint8(n * 255 / (ss * ss))
		img.SetNRGBA(i%size, i/size, c)
	}

	if err := os.MkdirAll("assets", 0o755); err != nil {
		panic(err)
	}
	f, err := os.Create("assets/icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
