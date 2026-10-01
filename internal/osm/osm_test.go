package osm

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
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

// TestMosaicFromDiskOnlySimulatesOfflineRestart: tiles on disk must
// fully serve a fresh Cache instance (app restart) with no network —
// every tile the mosaic needs is pre-planted as a solid red PNG, so a
// fully red result proves the disk path served everything.
func TestMosaicFromDiskOnlySimulatesOfflineRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "osmcache")
	// Plant tiles for exactly the coordinates Mosaic(640x480) requests.
	lat, lon, zoom := 13.5955, 100.56178, 10
	rx, ry := MercatorPx(lat, lon, zoom)
	x0, x1 := int(rx-320)/256, int(rx+320)/256
	y0, y1 := int(ry-240)/256, int(ry+240)/256
	red := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			red.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	layerDir := filepath.Join(dir, Layers[0].Name)
	os.MkdirAll(layerDir, 0o755)
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			var buf bytes.Buffer
			if err := png.Encode(&buf, red); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(layerDir, fmt.Sprintf("%d_%d_%d.tile", zoom, tx, ty)), buf.Bytes(), 0o644)
		}
	}
	// Fresh instance = app restarted; every tile is on disk so the
	// HTTP path is never taken.
	c := NewCache(dir)
	m := c.Mosaic(0, lat, lon, zoom, 640, 480)
	if m == nil {
		t.Fatal("mosaic nil")
	}
	nonRed := 0
	for y := 0; y < 480; y++ {
		for x := 0; x < 640; x++ {
			r, g, b, _ := m.At(x, y).RGBA()
			if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 {
				nonRed++
			}
		}
	}
	if nonRed > 0 {
		t.Fatalf("%d px not served from disk (gaps or network miss)", nonRed)
	}
}

func TestMercatorRoundTrip(t *testing.T) {
	for _, c := range [][2]float64{{13.5955, 100.56178}, {0, 0}, {-45.2, 172.9}, {51.5, -0.12}} {
		x, y := MercatorPx(c[0], c[1], 10)
		lat, lon := MercatorInv(x, y, 10)
		if math.Abs(lat-c[0]) > 1e-6 || math.Abs(lon-c[1]) > 1e-6 {
			t.Fatalf("round trip (%v,%v) -> (%v,%v)", c[0], c[1], lat, lon)
		}
	}
}
