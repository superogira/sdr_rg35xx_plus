package ui

import (
	"fmt"
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
	u.DrawRadar(blips, 100, "x:1", true, 13.5, 100.5, 12, nil, "", "", nil, t.TempDir(), LabelFlagText, 0, 0, 0, 0, 80, false, nil)
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
		u.DrawRadar(blips, 100, "x:1", true, 13.5, 100.5, 12, nil, "", "", nil, t.TempDir(), LabelFlagText, 0, 0, 0, 0, 80, false, nil)
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

// The selection must follow the SAME target while the drawn list
// re-sorts every frame (a fresh position report moves a ship's sort
// position) — the old index-only anchor jumped to neighbours.
func TestRadarSelectionFollowsResort(t *testing.T) {
	u := New(640, 480)
	_ = u.Frame(FrameStats{FreqHz: 1090000000, Mode: "AM"})
	sel := &RadarSel{On: true, Idx: 2}
	mk := func(ships [3]time.Time) []RadarBlip {
		var blips []RadarBlip
		for i, seen := range ships {
			blips = append(blips, RadarBlip{Vessel: true, Call: fmt.Sprintf("S%d", i), ICAO: fmt.Sprintf("56700000%d", i), HasPos: true, MercX: -160 + float64(i*20), MercY: 60, Seen: seen})
		}
		return blips
	}
	now := time.Now()
	// Select S2 (idx 2, newest), then re-sort so S0 becomes newest.
	u.DrawRadar(mk([3]time.Time{now.Add(-30 * time.Second), now.Add(-20 * time.Second), now}), 10, "x:1", true, 13.5, 100.5, 10, nil, "", "", nil, "", LabelFlagText, 0, 0, 0, 0, 80, false, sel)
	if sel.ID != "V567000002" {
		t.Fatalf("anchored %q, want V567000002", sel.ID)
	}
	// Fresh report on S0 → it jumps to the END of the draw order; the
	// selection must stay on S2.
	n := u.DrawRadar(mk([3]time.Time{now, now.Add(-20 * time.Second), now.Add(-10 * time.Second)}), 10, "x:1", true, 13.5, 100.5, 10, nil, "", "", nil, "", LabelFlagText, 0, 0, 0, 0, 80, false, sel)
	if sel.ID != "V567000002" {
		t.Fatalf("after resort selection drifted to %q (drawn=%d) — the 'moves by itself' bug", sel.ID, n)
	}
	// Walk the d-pad and verify the anchor updates.
	sel.Idx++
	u.DrawRadar(mk([3]time.Time{now, now.Add(-20 * time.Second), now.Add(-10 * time.Second)}), 10, "x:1", true, 13.5, 100.5, 10, nil, "", "", nil, "", LabelFlagText, 0, 0, 0, 0, 80, false, sel)
	if sel.ID == "V567000002" || sel.ID == "" {
		t.Fatalf("d-pad move did not re-anchor: %q", sel.ID)
	}
}
