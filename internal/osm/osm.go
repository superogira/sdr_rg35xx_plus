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
	_ "image/jpeg" // ESRI World Imagery serves JPEG tiles
	_ "image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MercMetresPx returns the ground metres per Mercator pixel at a
// latitude (the Mercator scale factor: the equator is compressed by
// cos(lat) into the same pixels).
func MercMetresPx(lat float64, zoom int) float64 {
	return 156543.03392 * math.Cos(lat*math.Pi/180) / math.Pow(2, float64(zoom))
}

// MercatorPx converts lat/lon to Web-Mercator world pixels at a zoom
// level (256 px per tile).
func MercatorPx(lat, lon float64, zoom int) (float64, float64) {
	n := 256.0 * math.Pow(2, float64(zoom))
	x := (lon + 180) / 360 * n
	latRad := lat * math.Pi / 180
	y := (1 - math.Log(math.Tan(latRad)+1/math.Cos(latRad))/math.Pi) / 2 * n
	return x, y
}

// Layer is one tile-server style. URL receives (zoom, x, y).
type Layer struct {
	Name    string
	URL     func(z, x, y int) string
	Attr    string
	NoFetch bool // the classic black radar — no tiles at all
}

// Layers available on the radar (L1/R1 cycles them). Indexes 0-2 are
// historical (ini adsblayer) — keep them stable and append new ones.
// Probed live and keyless: OSM, OpenTopoMap, and the ESRI arcgisonline
// family (JPEG). CARTO needs an API key (dropped); OpenFreeMap is
// vector-only (unusable for us); CyclOSM/OPNV/Wikimedia serve blanks
// or 403 to non-browser agents.
var Layers = []Layer{
	{"OSM", func(z, x, y int) string {
		return fmt.Sprintf("https://tile.openstreetmap.org/%d/%d/%d.png", z, x, y)
	}, "(c) OpenStreetMap contributors", false},
	{"Topo", func(z, x, y int) string {
		return fmt.Sprintf("https://tile.opentopomap.org/%d/%d/%d.png", z, x, y)
	}, "(c) OpenStreetMap contributors (SRTM | (c) OpenTopoMap (CC-BY-SA)", false},
	{"Sat", func(z, x, y int) string {
		// ESRI serves tiles in z/y/x order — and as JPEG.
		return fmt.Sprintf("https://server.arcgisonline.com/ArcGIS/rest/services/World_Imagery/MapServer/tile/%d/%d/%d", z, y, x)
	}, "Esri World Imagery", false},
	{"Street", func(z, x, y int) string {
		return fmt.Sprintf("https://server.arcgisonline.com/ArcGIS/rest/services/World_Street_Map/MapServer/tile/%d/%d/%d", z, y, x)
	}, "Esri World Street Map", false},
	{"EsriTopo", func(z, x, y int) string {
		return fmt.Sprintf("https://server.arcgisonline.com/ArcGIS/rest/services/World_Topo_Map/MapServer/tile/%d/%d/%d", z, y, x)
	}, "Esri World Topo Map", false},
	{"Phosphor", nil, "", true},
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

// tile fetches (memory → disk → network) one tile of a layer.
func (c *Cache) tile(layer, zoom, x, y int) *image.RGBA {
	key := fmt.Sprintf("%d/%d/%d/%d", layer, zoom, x, y)
	c.mu.Lock()
	img := c.mem[key]
	c.mu.Unlock()
	if img != nil {
		return img
	}

	dir := filepath.Join(c.dir, Layers[layer].Name)
	path := filepath.Join(dir, fmt.Sprintf("%d_%d_%d.tile", zoom, x, y)) // format-agnostic: PNG or JPEG bytes
	if b, err := os.ReadFile(path); err == nil {
		if img = decodePNG(b); img != nil {
			c.remember(key, img)
			return img
		}
	}
	_ = os.MkdirAll(dir, 0o755)

	req, err := http.NewRequest("GET", Layers[layer].URL(zoom, x, y), nil)
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
func (c *Cache) Mosaic(layer int, lat, lon float64, zoom, w, h int) *image.RGBA {
	rx, ry := MercatorPx(lat, lon, zoom)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	x0 := int(rx-float64(w)/2) / 256
	x1 := int(rx+float64(w)/2) / 256
	y0 := int(ry-float64(h)/2) / 256
	y1 := int(ry+float64(h)/2) / 256
	for ty := y0; ty <= y1; ty++ {
		for tx := x0; tx <= x1; tx++ {
			t := c.tile(layer, zoom, tx, ty)
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
