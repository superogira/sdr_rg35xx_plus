package ui

import (
	"image/png"
	"os"
	"testing"
)

func TestDrawFreqScale(t *testing.T) {
	u := New(640, 480)
	u.SetSpanKHz(100)
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	u.DrawFreqScale(21074000)

	// span 100k → half 50k → nice step 10k → ticks every 64 px from
	// the centre (320): 256/384 (n=1), 192/448 (n=2), 128/512 (n=3).
	tickAt := func(x int) bool {
		for y := 0; y < 14; y++ {
			r, g, b, _ := frame.At(x, y).RGBA()
			if r>>8 > 140 && g>>8 > 140 && b>>8 > 140 {
				return true
			}
		}
		return false
	}
	for _, x := range []int{128, 256, 384, 512} {
		if !tickAt(x) {
			t.Errorf("missing tick at x=%d", x)
		}
	}
	// Label pixels (bright text on dark backing) under the n=3 ticks.
	labelNear := func(x int) bool {
		for y := 20; y < 33; y++ {
			for dx := -45; dx < 45; dx++ {
				r, g, b, _ := frame.At(x+dx, y).RGBA()
				if r>>8 > 150 && g>>8 > 150 && b>>8 > 150 {
					return true
				}
			}
		}
		return false
	}
	if !labelNear(128) || !labelNear(512) {
		t.Error("frequency labels missing under 3rd ticks")
	}
	if os.Getenv("SDR_SCALESHOT") != "" {
		f, _ := os.Create("freqscale.png")
		defer f.Close()
		png.Encode(f, frame)
	}
}
