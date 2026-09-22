package ui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"

	"sdr35/internal/dsp"
)

// SavePNG writes a rendered frame to disk (the menu screenshot action).
func SavePNG(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// MenuItem is one row of the settings menu.
type MenuItem struct {
	Label string
	Value string
}

// blendByte mixes c into dst with alpha a (0-255).
func blendByte(dst, c uint8, a int) uint8 {
	return uint8((int(dst)*(255-a) + int(c)*a) / 255)
}

// fillBlend alpha-fills a rectangle over the composed frame.
func (u *UI) fillBlend(x, y, w, h int, r, g, b, a uint8) {
	for yy := y; yy < y+h; yy++ {
		if yy < 0 || yy >= u.H {
			continue
		}
		for xx := x; xx < x+w; xx++ {
			if xx < 0 || xx >= u.W {
				continue
			}
			o := yy*u.img.Stride + xx*4
			u.img.Pix[o+0] = blendByte(u.img.Pix[o+0], r, int(a))
			u.img.Pix[o+1] = blendByte(u.img.Pix[o+1], g, int(a))
			u.img.Pix[o+2] = blendByte(u.img.Pix[o+2], b, int(a))
			u.img.Pix[o+3] = 255
		}
	}
}

// DrawMenu renders the settings overlay on top of the composed frame.
func (u *UI) DrawMenu(items []MenuItem, sel int) {
	rowH := 28
	pw := 440
	ph := 64 + rowH*len(items) + 30
	px := (u.W - pw) / 2
	py := (u.H - ph) / 2

	u.fillBlend(px+4, py+4, pw, ph, 0, 0, 0, 120)
	u.fillBlend(px, py, pw, ph, 14, 20, 28, 242)

	white := color.RGBA{240, 240, 240, 255}
	grey := color.RGBA{150, 160, 170, 255}
	cyan := color.RGBA{80, 220, 255, 255}

	Face(17, true).DrawString(u.img, cyan, px+16, py+30, "ตั้งค่า — SETTINGS")
	hint := "A เลือก/ปรับ · B ปิด"
	Face(11, false).DrawString(u.img, grey, px+pw-16-Face(11, false).TextWidth(hint), py+30, hint)

	for i, it := range items {
		y := py + 56 + i*rowH
		if i == sel {
			u.fillBlend(px+8, y-18, pw-16, rowH-2, 40, 96, 128, 210)
		}
		f := Face(15, i == sel)
		f.DrawString(u.img, white, px+18, y, it.Label)
		vw := f.TextWidth(it.Value)
		vc := grey
		if i == sel {
			vc = color.RGBA{255, 230, 120, 255}
		}
		f.DrawString(u.img, vc, px+pw-18-vw, y, it.Value)
	}
}

// DrawFreqEditor renders the digit editor: digits is 9 characters
// (4 integer + 5 fractional MHz digits, the dot inserted when drawn);
// cursor is the selected digit index 0-8.
func (u *UI) DrawFreqEditor(digits string, cursor int) {
	disp := digits[:4] + "." + digits[4:]
	pw, ph := 460, 170
	px := (u.W - pw) / 2
	py := (u.H - ph) / 2

	u.fillBlend(px+4, py+4, pw, ph, 0, 0, 0, 120)
	u.fillBlend(px, py, pw, ph, 14, 20, 28, 242)

	white := color.RGBA{240, 240, 240, 255}
	grey := color.RGBA{150, 160, 170, 255}
	cyan := color.RGBA{80, 220, 255, 255}

	Face(16, true).DrawString(u.img, cyan, px+16, py+30, "ตั้งความถี่ (MHz)")
	Face(11, false).DrawString(u.img, grey, px+16, py+50,
		"↑↓ เปลี่ยนตัวเลข · ←→ เลื่อนหลัก · A ยืนยัน · B ยกเลิก")

	big := Face(40, true)
	totalW := big.TextWidth(disp)
	bx := px + (pw-totalW)/2
	by := py + 116
	if cursor < 0 || cursor > 8 {
		cursor = 6
	}
	// Digit i sits at display position i before the dot, i+1 after it
	// (the dot occupies position 4).
	pos := cursor
	if cursor >= 4 {
		pos = cursor + 1
	}
	preW := big.TextWidth(disp[:pos])
	chW := big.TextWidth(disp[:pos+1]) - preW
	u.fillBlend(bx+preW-1, by-34, chW+2, 46, 80, 220, 255, 90)
	big.DrawString(u.img, white, bx, by, disp)
}

// BarHeight is the bottom status bar; everything above it is waterfall.
const BarHeight = 64

// DisplaySpan is the half-width of spectrum shown, after cropping the
// filter transition band at the edges (see dsp.DisplaySpanHz).
const DisplaySpan = dsp.DisplaySpanHz

// UI owns the frame buffer and draws one screen per present.
type UI struct {
	W, H          int
	WaterfallRows int
	img           *image.RGBA
	// wf is the waterfall ALONE — the scrolling history never mixes with
	// the overlay drawings (labels, center line). Overlays used to be
	// drawn into the same buffer that scrolls, so each frame's text was
	// dragged down one row, leaving faint vertical trails under every
	// glyph — exactly the mysterious "lines" that flowed with the
	// waterfall. Now overlays are composed fresh on top of a copy of wf
	// every frame and can never persist into the history.
	wf *image.RGBA

	lut [256]color.RGBA

	// Spectrum state.
	snap  []complex128
	re    []float64
	im    []float64
	lastG uint64
	// Auto black level: slow-tracking noise floor (dB) so weak signals
	// still show color.
	floor float64

	stats FrameStats
}

// FrameStats is everything the bottom bar and overlays show.
type FrameStats struct {
	FreqHz      int64
	Mode        string
	StepHz      int64
	Connected   bool
	StatusText  string // Thai or English, one line
	PowerDb     float64
	Squelch     bool
	SquelchOpen bool
	Volume      float64
	GainText    string
	Host        string
}

func New(w, h int) *UI {
	u := &UI{W: w, H: h, WaterfallRows: h - BarHeight, floor: -95}
	u.img = image.NewRGBA(image.Rect(0, 0, w, h))
	u.wf = image.NewRGBA(image.Rect(0, 0, w, u.WaterfallRows))
	u.snap = make([]complex128, dsp.TapLen)
	u.re = make([]float64, dsp.TapLen)
	u.im = make([]float64, dsp.TapLen)
	u.initLUT()
	u.clearAll()
	return u
}

// initLUT builds a black→blue→red→yellow→white heat ramp.
func (u *UI) initLUT() {
	stops := []struct {
		pos float64
		c   [3]float64
	}{
		{0.00, [3]float64{0, 0, 8}},
		{0.20, [3]float64{24, 12, 80}},
		{0.45, [3]float64{64, 80, 200}},
		{0.65, [3]float64{210, 72, 60}},
		{0.82, [3]float64{250, 200, 70}},
		{1.00, [3]float64{255, 255, 255}},
	}
	for i := 0; i < 256; i++ {
		t := float64(i) / 255
		j := 0
		for j+1 < len(stops) && stops[j+1].pos < t {
			j++
		}
		a, b := stops[j], stops[j+1]
		f := (t - a.pos) / (b.pos - a.pos)
		if f < 0 {
			f = 0
		}
		u.lut[i] = color.RGBA{
			R: uint8(a.c[0] + f*(b.c[0]-a.c[0])),
			G: uint8(a.c[1] + f*(b.c[1]-a.c[1])),
			B: uint8(a.c[2] + f*(b.c[2]-a.c[2])),
			A: 255,
		}
	}
}

func (u *UI) clearAll() {
	black := color.RGBA{0, 0, 0, 255}
	for y := 0; y < u.H; y++ {
		for x := 0; x < u.W; x++ {
			u.img.SetRGBA(x, y, black)
		}
	}
	for y := 0; y < u.WaterfallRows; y++ {
		for x := 0; x < u.W; x++ {
			u.wf.SetRGBA(x, y, black)
		}
	}
}

// NewSpectrumRow scrolls the waterfall by one row if the tap has fresh
// samples and draws the newest spectrum on top. Returns true when the
// waterfall advanced.
func (u *UI) NewSpectrumRow(tap *dsp.SpectrumTap) bool {
	g := tap.Snapshot(u.snap)
	if g == 0 || g == u.lastG {
		return false
	}
	u.lastG = g

	n := len(u.snap)
	for i := 0; i < n; i++ {
		u.re[i] = real(u.snap[i])
		u.im[i] = imag(u.snap[i])
	}
	dsp.HannWindow(u.re, u.im)
	dsp.FFT(u.re, u.im)

	// Power per bin (fftshifted: index 0 = lowest frequency).
	power := make([]float64, n)
	for i := 0; i < n; i++ {
		power[i] = 20 * math.Log10(math.Hypot(u.re[(i+n/2)%n], u.im[(i+n/2)%n])+1e-12)
	}

	// Track the noise floor as the 25th percentile and normalize to it.
	sorted := append([]float64(nil), power...)
	bins := sorted
	for i := 1; i < len(bins); i++ { // tiny n, insertion sort is fine
		for j := i; j > 0 && bins[j] < bins[j-1]; j-- {
			bins[j], bins[j-1] = bins[j-1], bins[j]
		}
	}
	noise := bins[len(bins)/4]
	u.floor += 0.05 * (noise - u.floor)

	// Scroll the waterfall history down one row (the pure waterfall
	// buffer — no overlay ever lands in here).
	pix := u.wf.Pix
	stride := u.wf.Stride
	copy(pix[stride:], pix[:stride*(u.WaterfallRows-1)])

	// Draw the new row, stretching the displayed bin range across the
	// width.
	nVis := n * DisplaySpan / dsp.IF2Rate // half-width in bins
	if nVis < 1 || nVis > n/2 {
		nVis = n / 2
	}
	lo := n/2 - nVis
	hi := n/2 + nVis
	for x := 0; x < u.W; x++ {
		b0 := lo + x*(hi-lo)/u.W
		b1 := lo + (x+1)*(hi-lo)/u.W
		if b1 <= b0 {
			b1 = b0 + 1
		}
		if b1 > hi {
			b1 = hi
		}
		// Max-hold within the pixel.
		v := power[b0]
		for b := b0 + 1; b < b1 && b < hi; b++ {
			if power[b] > v {
				v = power[b]
			}
		}
		t := (v - u.floor - 6) / 62 // 62 dB of color above floor
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		c := u.lut[int(t*255)]
		o := x * 4
		pix[o] = c.R
		pix[o+1] = c.G
		pix[o+2] = c.B
		pix[o+3] = 255
	}
	return true
}

// Frame composes one screen: a fresh copy of the waterfall history with
// the overlays (center line, span labels) and the bottom bar drawn on top,
// then returns the image for presenting.
func (u *UI) Frame(stats FrameStats) *image.RGBA {
	u.stats = stats
	// Fresh waterfall copy: overlays from the previous frame are gone,
	// so they can never trail into the history.
	copy(u.img.Pix[:u.WaterfallRows*u.img.Stride], u.wf.Pix)
	u.drawCenterLine()
	u.drawSpanLabels()
	u.drawBottomBar()
	return u.img
}

func (u *UI) drawCenterLine() {
	x := u.W / 2
	for y := 0; y < u.WaterfallRows; y++ {
		if y%6 < 4 {
			u.img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
			u.img.SetRGBA(x+1, y, color.RGBA{255, 255, 255, 255})
		}
	}
}

func (u *UI) drawSpanLabels() {
	tiny := Face(13, false)
	white := color.RGBA{235, 235, 235, 255}
	shadow := color.RGBA{0, 0, 0, 220}
	centerMHz := float64(u.stats.FreqHz) / 1e6
	span := formatSpan(float64(DisplaySpan))
	center := fmt.Sprintf("%.4f MHz", centerMHz)
	labels := []struct {
		x int
		s string
	}{
		{4, "−" + span},
		{u.W/2 - tiny.TextWidth(center)/2, center},
		{u.W - tiny.TextWidth("+"+span) - 4, "+" + span},
	}
	for _, l := range labels {
		tiny.DrawString(u.img, shadow, l.x+1, 16, l.s)
		tiny.DrawString(u.img, white, l.x, 15, l.s)
	}
}

func formatSpan(hz float64) string {
	if hz >= 1e6 {
		return fmt.Sprintf("%.0fM", hz/1e6)
	}
	return fmt.Sprintf("%.0fk", hz/1e3)
}

func (u *UI) drawBottomBar() {
	pix := u.img.Pix
	stride := u.img.Stride
	// Panel background with a subtle top border.
	for y := u.WaterfallRows; y < u.H; y++ {
		for x := 0; x < u.W; x++ {
			var c color.RGBA
			switch {
			case y == u.WaterfallRows:
				c = color.RGBA{70, 130, 180, 255}
			case y == u.WaterfallRows+1:
				c = color.RGBA{18, 26, 34, 255}
			default:
				c = color.RGBA{12, 16, 22, 255}
			}
			o := y*stride + x*4
			pix[o], pix[o+1], pix[o+2], pix[o+3] = c.R, c.G, c.B, 255
		}
	}

	s := u.stats
	cyan := color.RGBA{80, 220, 255, 255}
	white := color.RGBA{240, 240, 240, 255}
	grey := color.RGBA{150, 160, 170, 255}

	// Big frequency (left).
	big := Face(38, true)
	freqMHz := float64(s.FreqHz) / 1e6
	dec := 3 // WFM shows kHz resolution, NFM 10 Hz
	if s.Mode == "NFM" {
		dec = 4
	}
	freqText := fmt.Sprintf("%.*f", dec, freqMHz)
	big.DrawString(u.img, white, 12, u.WaterfallRows+46, freqText)
	unitX := 16 + big.TextWidth(freqText)
	unit := Face(15, false)
	unit.DrawString(u.img, grey, unitX, u.WaterfallRows+44, "MHz")

	// Mode chip.
	chip := Face(16, true)
	chipText := s.Mode
	cw := chip.TextWidth(chipText) + 16
	cx := unitX + 44
	for y := u.WaterfallRows + 14; y < u.WaterfallRows+38; y++ {
		for x := cx; x < cx+cw; x++ {
			var c color.RGBA
			if s.Squelch && !s.SquelchOpen {
				c = color.RGBA{60, 40, 20, 255} // squelched: dim
			} else {
				c = color.RGBA{20, 70, 90, 255}
			}
			if x == cx || x == cx+cw-1 || y == u.WaterfallRows+14 || y == u.WaterfallRows+37 {
				c = color.RGBA{80, 200, 240, 255}
			}
			o := y*u.img.Stride + x*4
			u.img.Pix[o], u.img.Pix[o+1], u.img.Pix[o+2], u.img.Pix[o+3] = c.R, c.G, c.B, 255
		}
	}
	chip.DrawString(u.img, cyan, cx+8, u.WaterfallRows+31, chipText)

	// Signal strength bar with dB scale.
	barX, barW := cx+cw+16, 150
	barY, barH := u.WaterfallRows+18, 14
	label := Face(11, false)
	label.DrawString(u.img, grey, barX, barY-4, "SIGNAL dBFS")
	for x := 0; x < barW; x++ {
		t := float64(x) / float64(barW)
		db := -100 + t*80 // bar spans -100..-20 dBFS
		var c color.RGBA
		if db < s.PowerDb {
			c = color.RGBA{70, 200, 120, 255}
			if db > -45 {
				c = color.RGBA{255, 210, 80, 255}
			}
			if db > -32 {
				c = color.RGBA{255, 90, 70, 255}
			}
		} else {
			c = color.RGBA{35, 45, 55, 255}
		}
		o := barY*u.img.Stride + (barX+x)*4
		for y := 0; y < barH; y++ {
			oo := o + y*u.img.Stride
			u.img.Pix[oo], u.img.Pix[oo+1], u.img.Pix[oo+2], u.img.Pix[oo+3] = c.R, c.G, c.B, 255
		}
	}
	label.DrawString(u.img, grey, barX+barW+8, barY+12, fmt.Sprintf("%.0f", s.PowerDb))

	// Volume + gain (right side).
	right := Face(14, false)
	volX := u.W - 150
	volText := fmt.Sprintf("VOL %5.1f%%", s.Volume*100)
	right.DrawString(u.img, white, volX, u.WaterfallRows+18, volText)
	right.DrawString(u.img, grey, volX, u.WaterfallRows+38, s.GainText)

	// Status line (bottom).
	status := Face(13, false)
	col := grey
	if s.Connected {
		col = color.RGBA{120, 230, 140, 255}
	}
	statusText := s.StatusText
	if statusText == "" {
		if s.Connected {
			statusText = "กำลังฟัง " + s.Host
		} else {
			statusText = "ไม่ได้เชื่อมต่อ " + s.Host
		}
	}
	status.DrawString(u.img, col, 12, u.H-8, statusText)

	// Step + button hints (bottom right).
	hint := Face(12, false)
	hintText := "←→ จูน · SELECT โหมด · X sql · MENU เมนู (ค้าง 3 วิ = ออก)"
	hint.DrawString(u.img, grey, u.W-hint.TextWidth(hintText)-8, u.H-8, hintText)
}

func formatHz(hz int64) string {
	switch {
	case hz%1_000_000 == 0:
		return fmt.Sprintf("%dM", hz/1_000_000)
	case hz%100_000 == 0:
		return fmt.Sprintf("%dk", hz/1000)
	default:
		return fmt.Sprintf("%dk", hz/1000)
	}
}

var _ = fmt.Sprintf
