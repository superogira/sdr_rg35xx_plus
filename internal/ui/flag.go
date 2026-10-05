package ui

// The bundled flag icons come from catamphetamine/country-flag-icons
// (MIT — see flagicons/LICENSE): 3x2 set rendered to 18×12 PNG at build
// time. The runtime fetch below stays as a fallback for codes that are
// not in the bundled set.

import (
	"bytes"
	"embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

//go:embed flagicons/*.png
var flagEmbedded embed.FS

var (
	flagCache   = make(map[string]image.Image)
	flagPending = make(map[string]bool)
	flagMu      sync.Mutex
	flagClient  = &http.Client{Timeout: 10 * time.Second}
)

// FlagPNG returns the embedded flag PNG bytes for an ISO alpha-2
// code (for the web panel; empty when unknown).
func FlagPNG(cc string) []byte {
	if cc == "" || len(cc) != 2 {
		return nil
	}
	if data, err := flagEmbedded.ReadFile("flagicons/" + cc + ".png"); err == nil {
		return data
	}
	return nil
}

// GetFlag returns a 16×11 PNG flag icon for the given ISO 3166-1 alpha-2
// country code. Returns nil if unknown or still loading. Caches on disk
// under flagDir (e.g., ~/.../flags/TH.png).
func GetFlag(cc, flagDir string) image.Image {
	if cc == "" || len(cc) != 2 {
		return nil
	}
	flagMu.Lock()
	defer flagMu.Unlock()
	if img, ok := flagCache[cc]; ok {
		return img
	}
	if data, err := flagEmbedded.ReadFile("flagicons/" + cc + ".png"); err == nil {
		if img, err := png.Decode(bytes.NewReader(data)); err == nil {
			flagCache[cc] = img
			return img
		}
	}
	diskPath := filepath.Join(flagDir, cc+".png")
	if data, err := os.ReadFile(diskPath); err == nil {
		if img, err := png.Decode(bytes.NewReader(data)); err == nil {
			flagCache[cc] = img
			return img
		}
	}
	if !flagPending[cc] && len(flagPending) < 4 {
		flagPending[cc] = true
		go fetchFlag(cc, flagDir, diskPath)
	}
	return nil
}

func fetchFlag(cc, flagDir, diskPath string) {
	url := fmt.Sprintf("https://gitlab.com/catamphetamine/country-flag-icons/-/raw/master/flags/3x2/%s.svg", cc)
	resp, err := flagClient.Get(url)
	if err != nil {
		flagMu.Lock()
		delete(flagPending, cc)
		flagMu.Unlock()
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		flagMu.Lock()
		delete(flagPending, cc)
		flagMu.Unlock()
		return
	}
	svgData, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		flagMu.Lock()
		delete(flagPending, cc)
		flagMu.Unlock()
		return
	}
	svgData = rebaseViewBox(svgData)
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svgData))
	if err != nil {
		flagMu.Lock()
		delete(flagPending, cc)
		flagMu.Unlock()
		return
	}
	w, h := 18, 12
	icon.SetTarget(0, 0, float64(w), float64(h))
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	scanner := rasterx.NewScannerGV(w, h, rgba, rgba.Bounds())
	raster := rasterx.NewDasher(w, h, scanner)
	icon.Draw(raster, 1.0)

	os.MkdirAll(flagDir, 0755)
	if f, err := os.Create(diskPath); err == nil {
		png.Encode(f, rgba)
		f.Close()
	}
	flagMu.Lock()
	delete(flagPending, cc)
	flagCache[cc] = rgba
	flagMu.Unlock()
}

// rebaseViewBox rewrites a viewBox with a non-zero origin to (0,0) and
// shifts the geometry to match: oksvg silently draws nothing when the
// viewBox min-x/min-y are non-zero (flag-icons SVGs use e.g. 0,85.333).
func rebaseViewBox(svg []byte) []byte {
	i := bytes.Index(svg, []byte("viewBox=\""))
	if i < 0 {
		return svg
	}
	j := bytes.IndexByte(svg[i+9:], '"')
	if j < 0 {
		return svg
	}
	var x, y, w, h float64
	if _, err := fmt.Sscanf(string(svg[i+9:i+9+j]), "%f %f %f %f", &x, &y, &w, &h); err != nil || (x == 0 && y == 0) {
		return svg
	}
	out := make([]byte, 0, len(svg)+64)
	out = append(out, svg[:i+9]...)
	out = append(out, []byte(fmt.Sprintf("0 0 %g %g", w, h))...)
	out = append(out, '"')
	rest := svg[i+9+j+1:]
	if k := bytes.IndexByte(rest, '>'); k >= 0 {
		out = append(out, rest[:k+1]...)
		out = append(out, []byte(fmt.Sprintf("<g transform=\"translate(%g,%g)\">", -x, -y))...)
		rest = rest[k+1:]
		if m := bytes.LastIndex(rest, []byte("</svg>")); m >= 0 {
			out = append(out, rest[:m]...)
			out = append(out, []byte("</g>")...)
			rest = rest[m:]
		}
	}
	out = append(out, rest...)
	return out
}

// drawImage copies src onto dst at (x, y).
func drawImage(dst draw.Image, src image.Image, x, y int) {
	b := src.Bounds()
	draw.Draw(dst, image.Rect(x, y, x+b.Dx(), y+b.Dy()), src, b.Min, draw.Over)
}
