package ebitenmcp

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// sheetMaxWidth is what a contact sheet is scaled to fit.
//
// The cells are scaled before the sheet is composed, not after. Composing at
// native size and shrinking the result destroys the tick labels on any game
// above about 800 pixels wide: they are drawn at a fixed 13 pixels and end up
// four pixels tall, which is exactly the case the labels exist for.
const sheetMaxWidth = 1280

// contactSheet lays frames out in a grid, each labelled with its tick.
//
// This is the form in which motion can actually be read. A video file is for a
// person; a model gets one image, and a grid of moments costs the same as a
// single frame while showing what happened between them.
func contactSheet(frames []*Frame, columns int) *image.RGBA {
	if len(frames) == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	if columns <= 0 {
		columns = 4
	}
	if columns > len(frames) {
		columns = len(frames)
	}

	const (
		gap   = 6
		label = 14
	)

	native := frames[0].Image.Bounds()
	rows := (len(frames) + columns - 1) / columns

	// Fit the cells to the sheet's budget rather than the other way round.
	cellWidth := (sheetMaxWidth - (columns+1)*gap) / columns
	scale := 1.0
	if native.Dx() > cellWidth {
		scale = float64(cellWidth) / float64(native.Dx())
	}
	cell := image.Rect(0, 0,
		int(float64(native.Dx())*scale), int(float64(native.Dy())*scale))

	width := columns*cell.Dx() + (columns+1)*gap
	height := rows*(cell.Dy()+label) + (rows+1)*gap

	sheet := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.RGBA{R: 0x0d, G: 0x0f, B: 0x14, A: 0xff}}, image.Point{}, draw.Src)

	for i, frame := range frames {
		col, row := i%columns, i/columns

		x := gap + col*(cell.Dx()+gap)
		y := gap + row*(cell.Dy()+label+gap)

		at := image.Rect(x, y+label, x+cell.Dx(), y+label+cell.Dy())
		if scale == 1 {
			draw.Draw(sheet, at, frame.Image, frame.Image.Bounds().Min, draw.Src)
		} else {
			xdraw.CatmullRom.Scale(sheet, at, frame.Image, frame.Image.Bounds(), draw.Src, nil)
		}

		drawLabel(sheet, x+2, y+10, fmt.Sprintf("tick %d", frame.Tick))
	}

	return sheet
}

// drawLabel writes with the fixed bitmap font from x/image, which needs no
// asset files and keeps the example dependency-free.
func drawLabel(dst *image.RGBA, x, y int, text string) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  &image.Uniform{color.RGBA{R: 0x8a, G: 0x9a, B: 0xb8, A: 0xff}},
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)
}

// compareImages produces the before/after/difference strip.
//
// The difference panel keeps the "after" image dimmed underneath the changed
// pixels, so that what changed is legible in the context of where it changed
// rather than as an abstract mask.
func compareImages(before, after *image.RGBA) (*image.RGBA, int) {
	b := before.Bounds()
	if a := after.Bounds(); a.Dx() != b.Dx() || a.Dy() != b.Dy() {
		// The screen was resized between the two captures. Say so instead of
		// producing a comparison that lines nothing up.
		return after, -1
	}

	const (
		gap   = 6
		label = 14
	)

	w, h := b.Dx(), b.Dy()
	strip := image.NewRGBA(image.Rect(0, 0, 3*w+4*gap, h+label+2*gap))
	draw.Draw(strip, strip.Bounds(), &image.Uniform{color.RGBA{R: 0x0d, G: 0x0f, B: 0x14, A: 0xff}}, image.Point{}, draw.Src)

	panels := []struct {
		title string
		img   *image.RGBA
	}{
		{"before", before},
		{"after", after},
	}

	diff := image.NewRGBA(image.Rect(0, 0, w, h))
	changed := 0

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := after.PixOffset(x+b.Min.X, y+b.Min.Y)
			j := before.PixOffset(x+b.Min.X, y+b.Min.Y)

			same := before.Pix[j] == after.Pix[i] &&
				before.Pix[j+1] == after.Pix[i+1] &&
				before.Pix[j+2] == after.Pix[i+2]

			o := diff.PixOffset(x, y)
			if same {
				diff.Pix[o] = after.Pix[i] / 4
				diff.Pix[o+1] = after.Pix[i+1] / 4
				diff.Pix[o+2] = after.Pix[i+2] / 4
				diff.Pix[o+3] = 0xff
				continue
			}

			changed++
			diff.Pix[o], diff.Pix[o+1], diff.Pix[o+2], diff.Pix[o+3] = 0xff, 0x3b, 0x30, 0xff
		}
	}

	panels = append(panels, struct {
		title string
		img   *image.RGBA
	}{"difference", diff})

	for i, p := range panels {
		x := gap + i*(w+gap)

		drawLabel(strip, x+2, gap+10, p.title)
		draw.Draw(strip, image.Rect(x, gap+label, x+w, gap+label+h), p.img, p.img.Bounds().Min, draw.Src)
	}

	return strip, changed
}
