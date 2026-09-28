package osm

import (
	"math"
	"testing"
)

func TestMercatorPx(t *testing.T) {
	// Zoom 0: the whole world is one 256 px tile.
	x, y := MercatorPx(0, 0, 0)
	if math.Abs(x-128) > 0.01 || math.Abs(y-128) > 0.01 {
		t.Fatalf("origin: %.2f %.2f", x, y)
	}
	// Bangkok receiver at zoom 8.
	x, y = MercatorPx(13.5955, 100.56178, 8)
	n := 256.0 * 256
	wantX := (100.56178 + 180) / 360 * n
	if math.Abs(x-wantX) > 0.5 {
		t.Fatalf("x %.2f want %.2f", x, wantX)
	}
	// Symmetry: ±lat mirror around the equator line.
	_, ya := MercatorPx(13.6, 100.5, 10)
	_, yb := MercatorPx(-13.6, 100.5, 10)
	if math.Abs((ya+yb)-256.0*1024) > 1 {
		t.Fatalf("lat mirror broken: %.1f %.1f", ya, yb)
	}
}
