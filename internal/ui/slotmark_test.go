package ui

import (
	"image/png"
	"os"
	"testing"
)

func TestMarkFT8SlotTimestamp(t *testing.T) {
	u := New(640, 480)
	u.MarkFT8Slot("2026-09-23 14:11:30")
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})

	// Red line present under the strip.
	red := false
	for x := 0; x < 640; x += 8 {
		r, g, b, _ := frame.At(x, 13).RGBA()
		if r>>8 > 180 && g>>8 < 90 && b>>8 < 90 {
			red = true
			break
		}
	}
	if !red {
		t.Error("red separator row missing at y=13")
	}
	// Timestamp text pixels present in the strip (light gray on black).
	text := false
	for y := 2; y < 12 && !text; y++ {
		for x := 2; x < 200 && !text; x++ {
			r, g, b, _ := frame.At(x, y).RGBA()
			if r>>8 > 120 && g>>8 > 120 && b>>8 > 120 {
				text = true
			}
		}
	}
	if !text {
		t.Error("timestamp text missing in the strip above the red line")
	}
	if os.Getenv("SDR_SLOTSHOT") != "" {
		f, _ := os.Create("slotmark.png")
		defer f.Close()
		png.Encode(f, frame)
	}
}
