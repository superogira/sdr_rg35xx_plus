package ui

import (
	"image/png"
	"os"
	"testing"
)

func TestBracketRender(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   FrameStats
		off  float64
	}{
		{"fm_center", FrameStats{FreqHz: 145500000, LOHz: 145500000, BwHz: 12500, Mode: "NFM"}, 0},
		{"usb_offset", FrameStats{FreqHz: 21074200, LOHz: 21074000, BwHz: 2600, Mode: "USB", SSBOneSided: true}, 200},
		{"lsb_offset", FrameStats{FreqHz: 7074000, LOHz: 7075000, BwHz: 2600, Mode: "LSB"}, -1000},
	} {
		u := New(640, 480)
		u.SetSpanKHz(100)
		u.SetViewOff(tc.off)
		frame := u.Frame(tc.st)
		// cyan-ish pixels must exist somewhere in the waterfall region
		found := 0
		for y := 0; y < u.WaterfallRows; y++ {
			for x := 0; x < 640; x++ {
				r, g, b, _ := frame.At(x, y).RGBA()
				if r>>8 > 40 && g>>8 > 170 && b>>8 > 190 {
					found++
				}
			}
		}
		if found < 50 {
			t.Errorf("%s: bracket not rendered (%d px)", tc.name, found)
		}
		if os.Getenv("SDR_BRACKETSHOT") != "" {
			f, _ := os.Create("bracket_" + tc.name + ".png")
			png.Encode(f, frame)
			f.Close()
		}
	}
}
