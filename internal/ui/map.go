package ui

import (
	"image/color"
	"math"
	"time"

	"sdr35/internal/geo"
)

// MapEntry describes one FT8 activity point on the world map.
type MapEntry struct {
	Grid     string        // Maidenhead grid square
	IsCQ     bool          // true = CQ beacon, false = QSO exchange
	FromGrid string        // sender's grid for QSO arcs (empty if unknown)
	Age      time.Duration // time since decoded
}

// DrawWorldMap renders a world map with FT8 activity from the last minute.
// CQ messages show as expanding ripples over their grid location.
// QSO exchanges (signal reports, 73, etc.) show as dashed arcs between
// sender and recipient grids when both are known.
func (u *UI) DrawWorldMap(entries []MapEntry) {
	// Full-screen dark background
	u.fillBlend(0, 0, u.W, u.H, 15, 17, 23, 255)

	// Draw simplified world map outline (Equirectangular projection)
	u.drawMapOutline()

	// Draw activity markers
	for _, e := range entries {
		lat, lon, ok := geo.GridToLatLon(e.Grid)
		if !ok {
			continue
		}
		x, y := u.latLonToScreen(lat, lon)
		
		fade := 1.0 - (float64(e.Age) / (60 * float64(time.Second)))
		if fade < 0 {
			fade = 0
		}

		if e.IsCQ {
			// CQ: expanding ripple
			radius := float64(e.Age) / float64(time.Second) * 8 // 8px/second expansion
			u.drawRipple(x, y, radius, fade)
		} else if e.FromGrid != "" {
			// QSO: arc from sender to recipient
			fromLat, fromLon, okFrom := geo.GridToLatLon(e.FromGrid)
			if !okFrom {
				u.drawDot(x, y, fade)
				continue
			}
			x1, y1 := u.latLonToScreen(fromLat, fromLon)
			u.drawArc(x1, y1, x, y, fade)
		} else {
			// QSO but sender grid unknown: just a dot
			u.drawDot(x, y, fade)
		}
	}

	// Title overlay
	u.fillBlend(8, 8, 200, 28, 0, 0, 0, 200)
	tf := Face(14, false)
	tf.DrawString(u.img, color.RGBA{200, 200, 200, 255}, 16, 26, "FT8 World Map")
	
	// Hint bar
	hintY := u.H - 24
	u.fillBlend(0, hintY, u.W, 24, 0, 0, 0, 200)
	hint := "Any button to close"
	hw := Face(11, false).TextWidth(hint)
	Face(11, false).DrawString(u.img, color.RGBA{180, 180, 180, 255}, (u.W-hw)/2, hintY+17, hint)
}

// drawMapOutline draws a simplified world coastline (Equirectangular projection).
func (u *UI) drawMapOutline() {
	// Simplified continents as lat/lon polygons
	// This is a very basic outline - just major landmasses
	coastlines := [][]struct{ lat, lon float64 }{
		// Africa (simplified)
		{{35, -5}, {32, 10}, {15, 20}, {5, 40}, {-10, 40}, {-25, 30}, {-30, 20}, {-32, 15}, {-25, 15}, {-15, 10}, {0, 5}, {10, -10}, {20, -15}, {30, -10}, {35, -5}},
		// Europe (simplified)
		{{40, -10}, {45, 0}, {55, 5}, {60, 10}, {65, 20}, {70, 25}, {65, 30}, {55, 30}, {50, 35}, {45, 40}, {40, 35}, {38, 25}, {40, 15}, {40, -10}},
		// Asia (simplified)
		{{70, 30}, {65, 50}, {55, 80}, {50, 100}, {40, 120}, {35, 130}, {30, 140}, {20, 120}, {10, 100}, {0, 90}, {10, 70}, {20, 60}, {30, 50}, {40, 40}, {50, 35}, {60, 30}, {70, 30}},
		// Australia
		{{-10, 115}, {-15, 125}, {-25, 135}, {-35, 140}, {-38, 145}, {-35, 150}, {-28, 153}, {-20, 148}, {-15, 140}, {-12, 130}, {-10, 115}},
		// North America
		{{70, -100}, {65, -110}, {55, -130}, {50, -140}, {45, -125}, {40, -120}, {32, -115}, {30, -110}, {28, -95}, {25, -80}, {30, -75}, {35, -75}, {40, -70}, {45, -60}, {50, -55}, {55, -60}, {60, -70}, {65, -80}, {70, -100}},
		// South America
		{{10, -80}, {5, -75}, {-5, -70}, {-15, -72}, {-25, -70}, {-35, -65}, {-45, -70}, {-55, -68}, {-50, -75}, {-40, -75}, {-30, -80}, {-20, -78}, {-10, -78}, {0, -80}, {10, -80}},
	}

	lineColor := color.RGBA{60, 70, 80, 255}
	for _, coast := range coastlines {
		for i := 0; i < len(coast)-1; i++ {
			x1, y1 := u.latLonToScreen(coast[i].lat, coast[i].lon)
			x2, y2 := u.latLonToScreen(coast[i+1].lat, coast[i+1].lon)
			u.drawLine(x1, y1, x2, y2, lineColor)
		}
	}
}

// latLonToScreen converts lat/lon to screen coordinates (Equirectangular).
func (u *UI) latLonToScreen(lat, lon float64) (int, int) {
	// Equirectangular: x = lon, y = lat (with scaling)
	// lon: -180 to +180 → 0 to W
	// lat: +90 to -90 → 0 to H
	x := int((lon + 180) * float64(u.W) / 360)
	y := int((90 - lat) * float64(u.H) / 180)
	return x, y
}

// drawRipple draws an expanding circle (CQ beacon indicator).
func (u *UI) drawRipple(cx, cy int, radius, fade float64) {
	if radius < 1 {
		radius = 1
	}
	alpha := uint8(fade * 200)
	if alpha < 20 {
		alpha = 20
	}
	
	// Draw 3 concentric circles (ripple effect)
	for _, r := range []float64{radius, radius + 4, radius + 8} {
		if r > 50 {
			continue
		}
		c := color.RGBA{255, 200, 60, alpha}
		u.drawCircle(cx, cy, r, c)
	}
	
	// Center dot
	u.drawDot(cx, cy, fade)
}

// drawArc draws a great-circle arc (dashed line) between two points.
func (u *UI) drawArc(x1, y1, x2, y2 int, fade float64) {
	alpha := uint8(fade * 220)
	if alpha < 30 {
		alpha = 30
	}
	c := color.RGBA{100, 200, 255, alpha}
	
	// Simple dashed line (not true great circle, but good enough)
	dx := x2 - x1
	dy := y2 - y1
	steps := int(math.Sqrt(float64(dx*dx + dy*dy)))
	if steps < 2 {
		steps = 2
	}
	
	for i := 0; i < steps; i += 6 { // dash pattern: 6px line, 6px gap
		if i+3 > steps {
			break
		}
		t1 := float64(i) / float64(steps)
		t2 := float64(i+3) / float64(steps)
		sx := x1 + int(float64(dx)*t1)
		sy := y1 + int(float64(dy)*t1)
		ex := x1 + int(float64(dx)*t2)
		ey := y1 + int(float64(dy)*t2)
		u.drawLine(sx, sy, ex, ey, c)
	}
	
	// End markers
	u.drawDot(x1, y1, fade*0.7)
	u.drawDot(x2, y2, fade)
}

// drawDot draws a small filled circle.
func (u *UI) drawDot(cx, cy int, fade float64) {
	alpha := uint8(fade * 255)
	if alpha < 40 {
		alpha = 40
	}
	c := color.RGBA{255, 150, 80, alpha}
	
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			if dx*dx+dy*dy <= 4 {
				u.setPixel(cx+dx, cy+dy, c)
			}
		}
	}
}

// drawCircle draws a circle outline.
func (u *UI) drawCircle(cx, cy int, radius float64, c color.RGBA) {
	r := int(radius)
	if r < 1 {
		r = 1
	}
	
	// Bresenham-style circle
	x, y := 0, r
	d := 3 - 2*r
	
	for x <= y {
		u.setPixel(cx+x, cy+y, c)
		u.setPixel(cx-x, cy+y, c)
		u.setPixel(cx+x, cy-y, c)
		u.setPixel(cx-x, cy-y, c)
		u.setPixel(cx+y, cy+x, c)
		u.setPixel(cx-y, cy+x, c)
		u.setPixel(cx+y, cy-x, c)
		u.setPixel(cx-y, cy-x, c)
		
		if d < 0 {
			d += 4*x + 6
		} else {
			d += 4*(x-y) + 10
			y--
		}
		x++
	}
}

// drawLine draws a line between two points (Bresenham).
func (u *UI) drawLine(x1, y1, x2, y2 int, c color.RGBA) {
	dx := x2 - x1
	if dx < 0 {
		dx = -dx
	}
	dy := y2 - y1
	if dy < 0 {
		dy = -dy
	}
	
	sx := 1
	if x1 > x2 {
		sx = -1
	}
	sy := 1
	if y1 > y2 {
		sy = -1
	}
	
	err := dx - dy
	x, y := x1, y1
	
	for {
		u.setPixel(x, y, c)
		if x == x2 && y == y2 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x += sx
		}
		if e2 < dx {
			err += dx
			y += sy
		}
	}
}

// setPixel sets a single pixel if within bounds.
func (u *UI) setPixel(x, y int, c color.RGBA) {
	if x < 0 || x >= u.W || y < 0 || y >= u.H {
		return
	}
	o := y*u.img.Stride + x*4
	u.img.Pix[o+0] = c.R
	u.img.Pix[o+1] = c.G
	u.img.Pix[o+2] = c.B
	u.img.Pix[o+3] = c.A
}
