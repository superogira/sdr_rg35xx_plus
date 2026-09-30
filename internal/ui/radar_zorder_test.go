package ui

import (
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
	blips := []RadarBlip{
		{Call: "OLD1", ICAO: "880001", AltFt: 0, HasPos: true, MercX: -50, MercY: -50, Seen: time.Now().Add(-30 * time.Second)},
		{Call: "NEW9", ICAO: "880002", AltFt: 40000, SpdKt: 400, HasPos: true, MercX: -50, MercY: -50, Seen: time.Now()},
	}
	u.DrawRadar(blips, 100, "x:1", true, 13.5, 100.5, 12, nil, "", "", nil, t.TempDir())
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
