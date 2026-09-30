package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

var (
	flagCache   = make(map[string]image.Image)
	flagPending = make(map[string]bool)
	flagMu      sync.Mutex
	flagClient  = &http.Client{Timeout: 10 * time.Second}
)

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

// PrefetchAllFlags downloads every flag from the repo once (paced like
// the OSM tile fetcher) so the set works fully offline afterwards —
// ~250 icons at ~0.5 KB each. A marker file (all.done) stops repeats;
// misses stay retrievable through the normal on-demand path.
func PrefetchAllFlags(flagDir string) {
	marker := filepath.Join(flagDir, "all.done")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	codes, err := listFlagCodes()
	if err != nil {
		return // offline or API hiccup: retry on a later boot
	}
	os.MkdirAll(flagDir, 0755)
	fetched := 0
	for _, cc := range codes {
		diskPath := filepath.Join(flagDir, cc+".png")
		if _, err := os.Stat(diskPath); err == nil {
			continue
		}
		fetchFlag(cc, flagDir, diskPath)
		fetched++
		// Be a polite client even though this is a one-off warmup.
		time.Sleep(150 * time.Millisecond)
	}
	if f, err := os.Create(marker); err == nil {
		fmt.Fprintf(f, "%s %d\n", time.Now().UTC().Format(time.RFC3339), fetched)
		f.Close()
	}
}

// listFlagCodes lists the ISO codes available in the 3x2 directory of
// the flag-icons repository (paginated GitLab tree API).
func listFlagCodes() ([]string, error) {
	var codes []string
	for page := 1; page <= 10; page++ {
		u := fmt.Sprintf("https://gitlab.com/api/v4/projects/catamphetamine%%2Fcountry-flag-icons/repository/tree?path=flags%%2F3x2&per_page=100&page=%d", page)
		resp, err := flagClient.Get(u)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("tree api: %s", resp.Status)
		}
		var entries []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(body, &entries); err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			break
		}
		for _, e := range entries {
			if len(e.Name) == 6 && strings.HasSuffix(e.Name, ".svg") {
				codes = append(codes, e.Name[:2])
			}
		}
		if len(entries) < 100 {
			break
		}
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("no flags listed")
	}
	return codes, nil
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
