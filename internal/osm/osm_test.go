package osm

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"image"
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

func TestCacheClear(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "osmcache")
	c := NewCache(dir)
	os.MkdirAll(filepath.Join(dir, Layers[0].Name), 0755)
	c.remember("x", image.NewRGBA(image.Rect(0, 0, 1, 1)))
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cache dir still present: %v", err)
	}
	c.mu.Lock()
	n := len(c.mem)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("memory cache not emptied: %d", n)
	}
}
