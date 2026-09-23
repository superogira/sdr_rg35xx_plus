package ui

import (
	"math"
	"testing"

	"sdr35/internal/dsp"
)

// TestViewAlignsWithTone feeds a synthetic tone through the spectrum
// tap, pans the view onto it and checks the energy lands at screen
// centre — the regression: the pan moved the view only HALF the
// requested offset (n/2 vs n in the bin mapping), so the bracket and
// ruler drifted off the actual signal while scrolling.
func TestViewAlignsWithTone(t *testing.T) {
	dsp.SetIQRate(2_048_000)
	tap := dsp.NewSpectrumTap()
	const toneHz = 20000.0
	const rate = 256000 // IF2
	var phase float64
	for i := 0; i < 4096; i++ {
		re := 0.8 * math.Cos(phase)
		im := 0.8 * math.Sin(phase)
		phase += 2 * math.Pi * toneHz / rate
		tap.Push([]complex128{complex(re, im)})
	}
	u := New(640, 480)
	u.SetSpanKHz(100)
	u.SetViewOff(toneHz)
	u.NewSpectrumRow(tap, nil)
	frame := u.Frame(FrameStats{FreqHz: 21074000, LOHz: 21074000, Mode: "USB", BwHz: 2600})

	bestX, bestV := 0, -1.0
	for x := 0; x < 640; x++ {
		r, g, b, _ := frame.At(x, 0).RGBA()
		v := float64(r+g+b) / 3 / 257
		if v > bestV {
			bestV, bestX = v, x
		}
	}
	if bestV < 20 {
		t.Fatalf("waterfall row looks empty (best brightness %.1f at x=%d)", bestV, bestX)
	}
	if bestX < 640/2-20 || bestX > 640/2+20 {
		t.Fatalf("tone energy at x=%d (v=%.1f), want ~screen centre 320 (view pan misaligned)", bestX, bestV)
	}
}
