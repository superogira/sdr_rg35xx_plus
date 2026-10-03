package ui

import (
	"fmt"
	"image"
	"image/color"
)

// WefaxPanelW is the preview width in px. The chart keeps its aspect
// ratio (1809 columns per line), so the preview shows the newest
// rows at a fixed horizontal scale.
const WefaxPanelW = 200

// DrawWefaxPanel draws the live HF-FAX preview on the right side of
// the waterfall, above the clock/resource badges. preview is the
// decoder's downscaled view (newest row at the bottom); lines and
// state describe progress; hint is the key help line.
func (u *UI) DrawWefaxPanel(preview *image.Gray, lines int, state string, hint string) {
	tf := Face(11, false)
	const pad = 4
	pw := WefaxPanelW + 2*pad
	top := 4
	// leave room for the clock + resource badge stack (2 × 16 + gaps)
	bottom := u.WaterfallRows - 4 - 16 - 4 - 18 - 4
	ph := bottom - top
	if ph < 80 {
		return
	}
	px := u.W - pw - 4
	u.fillBlend(px, top, pw, ph, 0, 0, 0, 190)

	title := fmt.Sprintf("WEFAX · %s · %d", state, lines)
	tf.DrawString(u.img, color.RGBA{120, 220, 255, 255}, px+pad, top+13, title)

	// image area under the title, above the hint line
	ix, iy := px+pad, top+18
	ih := ph - 18 - 16
	if preview != nil && ih > 0 {
		b := preview.Bounds()
		for y := 0; y < ih; y++ {
			// bottom-align: the newest rows sit at the bottom of the box
			sy := b.Dy() - ih + y
			if sy < 0 {
				continue
			}
			for x := 0; x < WefaxPanelW && x < b.Dx(); x++ {
				g := preview.Pix[sy*preview.Stride+x]
				o := (iy+y)*u.img.Stride + (ix+x)*4
				if iy+y < 0 || iy+y >= u.H || ix+x < 0 || ix+x >= u.W {
					continue
				}
				u.img.Pix[o+0] = g
				u.img.Pix[o+1] = g
				u.img.Pix[o+2] = g
				u.img.Pix[o+3] = 255
			}
		}
	}
	tf.DrawString(u.img, color.RGBA{150, 170, 150, 255}, px+pad, top+ph-5, hint)
}

// WefaxPreviewRows is how many preview rows the panel can show at the
// current screen size — main asks the decoder for exactly this many.
func (u *UI) WefaxPreviewRows() int {
	top := 4
	bottom := u.WaterfallRows - 4 - 16 - 4 - 18 - 4
	return bottom - top - 18 - 16
}
// DrawWefaxGuides marks where the fax subcarrier must sit: the black
// tone at +1500 Hz and the white tone at +2300 Hz from the dial (USB
// with the dial 1.9 kHz below the assigned frequency). Tune until the
// signal's two tone bands line up with the B and W dashed lines.
func (u *UI) DrawWefaxGuides() {
	s := u.stats
	off := float64(s.FreqHz-s.LOHz) - u.viewOffHz
	pxPerHz := float64(u.W) / float64(u.SpanFull)
	x := func(dhz float64) int {
		return u.W/2 + int((off+dhz)*pxPerHz+0.5)
	}
	x0, x1 := x(1500), x(2300)
	if x1-x0 < 4 {
		return // span too wide for the guides to mean anything
	}
	amber := color.RGBA{250, 210, 80, 255}
	// dashed verticals: 6 px on, 4 px off
	for _, xx := range []int{x0, x1} {
		for y := 0; y < u.WaterfallRows; y += 10 {
			for dy := 0; dy < 6 && y+dy < u.WaterfallRows; dy++ {
				if xx < 0 || xx >= u.W || y+dy < 0 {
					continue
				}
				o := (y+dy)*u.img.Stride + xx*4
				u.img.Pix[o+0], u.img.Pix[o+1], u.img.Pix[o+2], u.img.Pix[o+3] = amber.R, amber.G, amber.B, 255
			}
		}
	}
	// labels at the top of the waterfall: B = black tone, W = white
	tf := Face(10, false)
	tf.DrawString(u.img, amber, x0-3, 10, "B")
	tf.DrawString(u.img, amber, x1-3, 10, "W")
}
