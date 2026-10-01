package ui

import (
	"image"
	"image/color"
	"testing"
	"time"
)

// Two aircraft parked on the same spot: the freshest Seen must paint its
// label on top. Before the deterministic sort, blips arrived in map
// order, reshuffled every frame, and the overlapping labels flickered.
func TestRadarNewestLabelOnTop(t *testing.T) {
	u := New(640, 480)
	img := u.Frame(FrameStats{FreqHz: 1090000000, Mode: "AM"})
	newer, older := altitudeColor(40000), altitudeColor(0)
	// NEW deliberately FIRST in the slice: with the sort running before
	// the draw loop it must still paint last (on top). This catches the
	// sort being dead code, which is exactly what happened before.
	blips := []RadarBlip{
		{Call: "NEW9", ICAO: "880002", AltFt: 40000, SpdKt: 400, HasPos: true, MercX: -50, MercY: -50, Seen: time.Now()},
		{Call: "OLD1", ICAO: "880001", AltFt: 0, HasPos: true, MercX: -50, MercY: -50, Seen: time.Now().Add(-30 * time.Second)},
	}
	u.DrawRadar(blips, 100, "x:1", true, 13.5, 100.5, 12, nil, "", "", nil, t.TempDir(), LabelFlagText, 0, 0, 0, 0, 80, -1)
	nNew, nOld := 0, 0
	for y := 0; y < u.H; y++ {
		for x := 0; x < u.W; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if near(uint8(r>>8), uint8(g>>8), uint8(b>>8), newer) {
				nNew++
			}
			if near(uint8(r>>8), uint8(g>>8), uint8(b>>8), older) {
				nOld++
			}
		}
	}
	if nNew < 20 {
		t.Fatalf("newest label not drawn on top (new-color px=%d)", nNew)
	}
	if nOld > 5 {
		t.Fatalf("stale label bleeds through (old-color px=%d)", nOld)
	}
}

func near(r, g, b uint8, c color.RGBA) bool {
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(r, c.R) < 40 && d(g, c.G) < 40 && d(b, c.B) < 40
}

// A target beyond the selected range must not draw anything: the frame
// with one such blip has to be identical to an empty radar (the header
// counts drawn blips only). Retry guards the seconds-tick of the clock.
func TestRadarOutOfRangeHidden(t *testing.T) {
	u := New(640, 480)
	mk := func(blips []RadarBlip) *image.RGBA {
		img := u.Frame(FrameStats{FreqHz: 1090000000, Mode: "AM"})
		u.DrawRadar(blips, 100, "x:1", true, 13.5, 100.5, 12, nil, "", "", nil, t.TempDir(), LabelFlagText, 0, 0, 0, 0, 80, -1)
		return img
	}
	far := []RadarBlip{
		{Call: "FAR1", ICAO: "880003", AltFt: 35000, HasPos: true, MercX: 400, MercY: 0, DistKm: 400, BrngDeg: 90, Seen: time.Now()},
		{Vessel: true, Call: "FARSHP", ICAO: "567999999", HasPos: true, MercX: -400, MercY: 0, DistKm: 400, BrngDeg: 270, Seen: time.Now()},
	}
	for attempt := 0; attempt < 3; attempt++ {
		a := mk(far)
		b := mk(nil)
		if sameImage(a, b) {
			return
		}
		time.Sleep(1100 * time.Millisecond) // clock probably ticked; retry
	}
	t.Fatal("out-of-range blips still visible")
}

func sameImage(a, b *image.RGBA) bool {
	if a.Rect != b.Rect {
		return false
	}
	for y := a.Rect.Min.Y; y < a.Rect.Max.Y; y++ {
		for x := a.Rect.Min.X; x < a.Rect.Max.X; x++ {
			if a.At(x, y) != b.At(x, y) {
				return false
			}
		}
	}
	return true
}
