package ui

import (
	"math"
	"testing"
)

// TestRadarMapProjectionSpreadsTargets guards the regression where every
// blip collapsed onto ONE screen point once a map layer was active: the
// target positions were computed at a different Mercator zoom than the
// receiver reference, so the world-pixel difference was enormous and
// every blip hit the edge clamp at the same place.
func TestRadarMapProjectionSpreadsTargets(t *testing.T) {
	const zoom = 9
	mPerPx := 156543.03392 * math.Cos(13.5955*math.Pi/180) / math.Pow(2, zoom)

	mercPx := func(lat, lon float64) (float64, float64) {
		n := 256.0 * math.Pow(2, zoom)
		x := (lon + 180) / 360 * n
		lr := lat * math.Pi / 180
		y := (1 - math.Log(math.Tan(lr)+1/math.Cos(lr))/math.Pi) / 2 * n
		return x, y
	}
	rLat, rLon := 13.5955, 100.56178
	rx, ry := mercPx(rLat, rLon)
	m := &MercView{Zoom: zoom, Rx: rx, Ry: ry, MPerPx: mPerPx}

	// Targets around Bangkok: river mouth, city centre, north, south.
	targets := [][2]float64{
		{13.65, 100.60}, {13.58, 100.52}, {13.75, 100.62}, {13.45, 100.66},
	}
	seen := map[[2]int]bool{}
	var maxOff float64
	for _, tg := range targets {
		mx, my := mercPx(tg[0], tg[1])
		dx, dy := m.project(mx, my)
		key := [2]int{dx, dy}
		if seen[key] {
			t.Fatalf("targets collapse to the same point %+v", key)
		}
		seen[key] = true
		off := math.Hypot(float64(dx), float64(dy))
		if off > maxOff {
			maxOff = off
		}
		// Every target must land ON screen (a map view never clamps).
		if off > 250 {
			t.Fatalf("target %.2f,%.2f lands %0.f px off centre — wrong zoom", tg[0], tg[1], off)
		}
	}
	if len(seen) != len(targets) {
		t.Fatalf("only %d distinct positions from %d targets", len(seen), len(targets))
	}
	// Sanity: 10 km east should be ~10 000/mPerPx px from centre.
	dLat := rLat + 10/111.32
	dLon := rLon + 10/(111.32*math.Cos(rLat*math.Pi/180))
	mx, my := mercPx(dLat, dLon)
	dx, dy := m.project(mx, my)
	want := 10000 / mPerPx
	if math.Abs(float64(dx)-want) > 1 || math.Abs(float64(dy)+want) > 1 {
		t.Fatalf("(+10km E, +10km N) = (%d, %d), want ~(%d, %d)", dx, dy, int(want), -int(want))
	}
	_ = maxOff
}
