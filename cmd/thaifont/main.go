package main

import (
	"image"
	"image/color"
	"image/png"
	"os"

	"sdr35/internal/ui"
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, 640, 220))
	for y := 0; y < 220; y++ {
		for x := 0; x < 640; x++ {
			img.SetRGBA(x, y, color.RGBA{12, 16, 22, 255})
		}
	}
	white := color.RGBA{240, 240, 240, 255}
	cyan := color.RGBA{80, 220, 255, 255}
	ui.Face(34, true).DrawString(img, white, 20, 50, "กำลังฟังวิทยุ 145.500 MHz")
	ui.Face(28, false).DrawString(img, cyan, 20, 100, "ขั้นความถี่ 12.5k โหมด NFM")
	ui.Face(28, false).DrawString(img, white, 20, 150, "ไม่ได้เชื่อมต่อ ลองใหม่ใน 3 วินาที")
	ui.Face(24, false).DrawString(img, white, 20, 195, "SDR สำหรับ RG35XX Plus")
	f, _ := os.Create("thai_test.png")
	defer f.Close()
	png.Encode(f, img)
}
