// mapcheck — dev tool: verifies every maps/*.png shares the world map's
// geography (same projection & alignment). Compares coastline edge maps
// at coarse resolution; the reference is world_map_2.png (landmark-
// verified equirectangular). A map passes when its edge structure
// correlates best at offset (0,0) with a clear margin. Working on
// gradients (not raw luminance) makes it immune to smooth color
// gradients and to land/ocean brightness inversions between themes.
// Run: go run ./cmd/mapcheck
package main

import (
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
)

const W, H = 80, 60 // downscale target

func edgeMap(path string) ([]float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	// Box-average downscale to W×H luminance (blurs away intra-land texture).
	lum := make([]float64, W*H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			var sum float64
			var n int
			x0, x1 := b.Dx()*x/W, b.Dx()*(x+1)/W
			y0, y1 := b.Dy()*y/H, b.Dy()*(y+1)/H
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					r, g, bl, _ := img.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
					n++
				}
			}
			lum[y*W+x] = sum / float64(n)
		}
	}
	// Gradient magnitude keeps coastlines, kills smooth gradients.
	e := make([]float64, W*H)
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			i := y*W + x
			gx := lum[i+1] - lum[i-1]
			gy := lum[i+W] - lum[i-W]
			e[i] = math.Sqrt(gx*gx + gy*gy)
		}
	}
	return e, nil
}

// pearson computes |r| between ref and m with m shifted by (dx,dy),
// over the region where both are valid.
func pearson(ref, m []float64, dx, dy int) float64 {
	var n int
	var sa, sb, saa, sbb, sab float64
	for y := 6; y < H-6; y++ {
		jy := y + dy
		if jy < 0 || jy >= H {
			continue
		}
		for x := 6; x < W-6; x++ {
			jx := x + dx
			if jx < 0 || jx >= W {
				continue
			}
			a := ref[y*W+x]
			bv := m[jy*W+jx]
			n++
			sa += a
			sb += bv
			saa += a * a
			sbb += bv * bv
			sab += a * bv
		}
	}
	if n < 100 {
		return 0
	}
	meanA, meanB := sa/float64(n), sb/float64(n)
	cov := sab/float64(n) - meanA*meanB
	va := saa/float64(n) - meanA*meanA
	vb := sbb/float64(n) - meanB*meanB
	if va <= 0 || vb <= 0 {
		return 0
	}
	r := cov / math.Sqrt(va*vb)
	if r < 0 {
		r = -r
	}
	return r
}

func main() {
	files, err := filepath.Glob("maps/*.png")
	if err != nil || len(files) == 0 {
		fmt.Println("no maps found:", err)
		os.Exit(1)
	}
	sort.Strings(files)
	ref, err := edgeMap("maps/world_map_2.png")
	if err != nil {
		fmt.Println("reference:", err)
		os.Exit(1)
	}
	bad := 0
	for _, f := range files {
		m, err := edgeMap(f)
		if err != nil {
			fmt.Printf("%-28s DECODE ERROR %v\n", f, err)
			bad++
			continue
		}
		r00 := pearson(ref, m, 0, 0)
		best, bx, by := -1.0, 0, 0
		for dy := -3; dy <= 3; dy++ {
			for dx := -3; dx <= 3; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				if v := pearson(ref, m, dx, dy); v > best {
					best, bx, by = v, dx, dy
				}
			}
		}
		status := "OK"
		if !(r00 > best+0.03 && r00 > 0.2) {
			status = fmt.Sprintf("MISMATCH (rival %.3f at %+d,%+d)", best, bx, by)
			bad++
		}
		fmt.Printf("%-28s r@00=%.3f  %s\n", f, r00, status)
	}
	if bad > 0 {
		fmt.Printf("\n%d suspicious map(s)\n", bad)
		os.Exit(1)
	}
	fmt.Println("\nall maps align with the reference")
}
