// genicon draws the APPS-menu icon (SDR35.png) with the same font/display
// stack as the app itself.
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"

	"sdr35/internal/ui"
)

func main() {
	const size = 144
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// Background: dark blue gradient.
	for y := 0; y < size; y++ {
		t := float64(y) / size
		for x := 0; x < size; x++ {
			r := uint8(8 + 10*t)
			g := uint8(16 + 30*t)
			b := uint8(40 + 60*t)
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	// Waterfall stripes.
	for y := 18; y < 84; y++ {
		for x := 14; x < size-14; x++ {
			var c color.RGBA
			switch {
			case x > 96 && x < 112:
				c = color.RGBA{250, 200, 70, 255} // signal
			case x > 46 && x < 54:
				c = color.RGBA{200, 80, 60, 255}
			case x > 120:
				c = color.RGBA{70, 80, 160, 255}
			default:
				v := uint8(20 + ((x*7 + y*3) / 4 % 40))
				c = color.RGBA{v / 3, v / 2, v, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	// Text.
	ui.Face(40, true).DrawString(img, color.RGBA{240, 240, 240, 255}, 18, 128, "SDR")
	ui.Face(19, false).DrawString(img, color.RGBA{80, 220, 255, 255}, 92, 128, "g35xx")

	f, err := os.Create("rg35xx/SDRg35xx.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
