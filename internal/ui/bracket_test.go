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

// TestViewHysteresis: the view pans only when the bracket would leave
// the edge band — scrolling back must leave the view where it was.
func TestViewHysteresis(t *testing.T) {
	u := New(640, 480)
	u.SetSpanKHz(100)
	// scroll right in 1 kHz steps from centre
	for i := 1; i <= 30; i++ {
		u.SetViewOff(u.ViewOffHzSmooth(float64(i) * 1000))
	}
	v1 := u.viewOffHz
	// bracket now rides the right edge; scroll back 10 kHz
	for i := 30; i >= 20; i-- {
		u.SetViewOff(u.ViewOffHzSmooth(float64(i) * 1000))
	}
	if u.viewOffHz != v1 {
		t.Errorf("view moved while scrolling back: %v -> %v", v1, u.viewOffHz)
	}
	// scroll back to the centre: view must stay (bracket well inside)
	for i := 20; i >= 10; i-- {
		u.SetViewOff(u.ViewOffHzSmooth(float64(i) * 1000))
	}
	if u.viewOffHz != v1 {
		t.Errorf("view moved while bracket inside the edge band: %v -> %v", v1, u.viewOffHz)
	}
	// scroll far LEFT past the other edge: view must follow down
	for i := 10; i >= -40; i-- {
		u.SetViewOff(u.ViewOffHzSmooth(float64(i) * 1000))
	}
	if u.viewOffHz > -1000 {
		t.Errorf("view did not follow the leftward scroll: %v", u.viewOffHz)
	}
}
