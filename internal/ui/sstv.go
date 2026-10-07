package ui

import (
	"fmt"
	"image"
	"image/color"
)

// DrawSSTV renders the slow-scan TV picture panel: the in-progress or
// finished raster centred at native 320x256 with a status line.
func (u *UI) DrawSSTV(img *image.NRGBA, name string, line, total int) {
	x, y := (u.W-320)/2, 24
	u.fillBlend(x-6, y-20, 332, 300, 0, 0, 0, 215)
	tf := Face(13, true)
	hint := Face(11, false)
	status := name
	if status == "" {
		status = "—"
	}
	if total > 0 {
		status = fmt.Sprintf("%s  %d/%d", status, line, total)
	}
	tf.DrawString(u.img, color.RGBA{120, 220, 255, 255}, x, y-6, "SSTV  "+status)
	if img == nil {
		hint.DrawString(u.img, color.RGBA{150, 180, 150, 255}, x+40, y+120, "· · ·")
		return
	}
	b := img.Bounds()
	for yy := 0; yy < b.Dy(); yy++ {
		for xx := 0; xx < b.Dx(); xx++ {
			c := img.NRGBAAt(xx, yy)
			u.setPixel(x+xx, y+yy, color.RGBA{c.R, c.G, c.B, 255})
		}
	}
	hint.DrawString(u.img, color.RGBA{150, 180, 150, 255}, x, y+b.Dy()+16, "B back")
}
