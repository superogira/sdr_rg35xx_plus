// Package osm fetches and caches OpenStreetMap raster tiles and builds
// screen-sized mosaics for the ADS-B radar background. The radar view
// is static (receiver fixed at the screen centre), so each zoom level
// needs only the handful of tiles covering the screen — fetched once,
// cached on disk, reused forever.
package osm

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MercatorPx converts lat/lon to Web-Mercator world pixels at a zoom
// level (256 px per tile).
func MercatorPx(lat, lon float64, zoom int) (float64, float64) {
	n := 256.0 * math.Pow(2, float64(zoom))
	x := (lon + 180) / 360 * n
	latRad := lat * math.Pi / 180
	y := (1 - math.Log(math.Tan(latRad)+1/math.Cos(latRad))/math.Pi) / 2 * n
	return x, y
}

// Cache keeps tile images in memory and on disk.
type Cache struct {
	mu     sync.Mutex
	dir    string
	mem    map[string]*image.RGBA
	client *http.Client
}

func NewCache(dir string) *Cache {
	return &Cache{
		dir:    dir,
		mem:    map[string]*image.RGBA{},
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// tile fetches (memory → disk → network) one tile.
func (c *Cache) tile(zoom, x, y int) *image.RGBA {
	key := fmt.Sprintf("%d/%d/%d", zoom, x, y)
	c.mu.Lock()
	img := c.mem[key]
	c.mu.Unlock()
	if img != nil {
		return img
	}

	path := filepath.Join(c.dir, fmt.Sprintf("%d_%d_%d.png", zoom, x, y))
	if b, err := os.ReadFile(path); err == nil {
		if img = decodePNG(b); img != nil {
			c.remember(key, img)
			return img
		}
	}
	_ = os.MkdirAll(c.dir, 0o755)

	req, err := http.NewRequest("GET", fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", zoom, x, y), nil)
	if err != nil {
		return nil
	}
	// OSM tile usage policy: an identifying User-Agent is required.
	req.Header.Set("User-Agent", "SDRg35xx/1.0 (ham radio receiver; occasional static tiles)")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil
	}
	img = decodePNG(b)
	if img == nil {
		return nil
	}
	c.remember(key, img)
	os.WriteFile(path, b, 0o644)
	// Politeness: never hammer the tile server.
	time.Sleep(120 * time.Millisecond)
	return img
}

func (c *Cache) remember(key string, img *image.RGBA) {
	c.mu.Lock()
	c.mem[key] = img
	c.mu.Unlock()
}

func decodePNG(b []byte) *image.RGBA {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	rgba, ok := img.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
		draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	}
	return rgba
}

// Mosaic builds a w×h image centred on lat/lon at a zoom level,
// pulling the covering tiles. Missing tiles leave black gaps.
func (c *Cache) Mosaic(lat, lon float64, zoom, w, h int) *image.RGBA {
	rx, ry := MercatorPx(lat, lon, zoom)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	x0 := int(rx-float64(w)/2) / 256
	x1 := int(rx+float64(w)/2) / 256
	y0 := int(ry-float64(h)/2) / 256
	y1 := int(ry+float64(h)/2) / 256
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			t := c.tile(zoom, tx, ty)
			if t == nil {
				continue
			}
			ox := tx*256 - int(rx) + w/2
			oy := ty*256 - int(ry) + h/2
			draw.Draw(out, image.Rect(ox, oy, ox+256, oy+256), t, t.Bounds().Min, draw.Src)
		}
	}
	return out
}

// HasTiles reports whether every tile of the mosaic came back (used to
// decide map vs fallback rendering — a half-empty fetch is fine to
// show, gaps are just dark).
func (c *Cache) MosaicComplete(lat, lon float64, zoom, w, h int, m *image.RGBA) bool {
	rx, ry := MercatorPx(lat, lon, zoom)
	x0 := int(rx-float64(w)/2) / 256
	x1 := int(rx+float64(w)/2) / 256
	y0 := int(ry-float64(h)/2) / 256
	y1 := int(ry+float64(h)/2) / 256
	need := (x1 - x0 + 1) * (y1 - y0 + 1)
	if need == 0 {
		return false
	}
	// Count non-black pixels as a cheap completeness proxy: a fully
	// fetched mosaic is never entirely black.
	dark := 0
	for y := 0; y < h; y += 16 {
		for x := 0; x < w; x += 16 {
			r, g, b, _ := m.At(x, y).RGBA()
			if r>>8 < 6 && g>>8 < 6 && b>>8 < 6 {
				dark++
			}
		}
	}
	return dark < (w/16)*(h/16)*3/4
}
