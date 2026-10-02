package dsp

import (
	"image"
	"math"
	"sync"
)

// WEFAX (HF weather facsimile) decoder.
//
// Signal: FM of an audio subcarrier centred on 1900 Hz, ±400 Hz —
// 1500 Hz = black, 2300 Hz = white (tuned in USB with the dial 1900 Hz
// below the assigned carrier, i.e. "dial = carrier − 1.9 kHz").
// Standard: 120 lines/min (2 lines/s), IOC 576 → 1809 pixels per line
// (576·π). Start/stop are APT tones keyed on the same subcarrier:
// 300 Hz black/white toggling = start (IOC 576), 450 Hz = stop.
// A phasing period (a short white pulse at the start of each line)
// precedes the image so the receiver can find the left edge.
//
// Input is the 8 kHz real monitor stream (same as RTTY/FT8).

const (
	WefaxRate     = 8000
	WefaxLPM      = 120
	WefaxWidth    = 1809 // IOC 576 · π
	WefaxMaxLines = 1400 // typical charts are 800-1200 lines
	wefaxCentre   = 1900.0
	wefaxDev      = 400.0
)

// WefaxState is the decoder's high-level phase.
type WefaxState int

const (
	WefaxIdle    WefaxState = iota // listening for start tone / free-running
	WefaxPhasing                   // start tone heard, aligning on phasing pulses
	WefaxImage                     // receiving lines
)

// WefaxDecoder demodulates and rasterises one fax at a time.
type WefaxDecoder struct {
	mu sync.Mutex

	// demod
	ph       float64 // mixer phase
	lpI, lpQ []float64
	lpTaps   []float64
	lpPos    int
	prevI    float64
	prevQ    float64
	lumaLP   float64

	// raster
	samplesPerLine float64
	pos            float64 // fractional sample index within the current line
	line           []float64
	img            *image.Gray
	lines          int
	state          WefaxState
	freeRun        bool // draw without an APT start (mid-transmission tune-in)

	// APT tone detector: count black/white transitions per window
	aptWin     int
	aptTrans   int
	aptLast    bool
	aptStartN  int // consecutive windows with ~300 Hz toggling
	aptStopN   int // consecutive windows with ~450 Hz toggling
	stopAtLine int // image line count when the stop tone began
	phasingAcc []float64
	phasingN   int

	// completed images waiting for the caller to save
	done []*image.Gray
}

// NewWefaxDecoder returns a decoder ready to free-run (so tuning into a
// chart already in progress still shows something immediately).
func NewWefaxDecoder() *WefaxDecoder {
	d := &WefaxDecoder{freeRun: true}
	d.lpTaps = DesignLowpass(63, 600, WefaxRate)
	d.lpI = make([]float64, len(d.lpTaps))
	d.lpQ = make([]float64, len(d.lpTaps))
	d.samplesPerLine = WefaxRate * 60.0 / WefaxLPM // 4000
	d.line = make([]float64, WefaxWidth)
	d.reset()
	return d
}

func (d *WefaxDecoder) reset() {
	d.img = image.NewGray(image.Rect(0, 0, WefaxWidth, WefaxMaxLines))
	for i := range d.img.Pix {
		d.img.Pix[i] = 255
	}
	d.lines = 0
	d.pos = 0
	d.state = WefaxIdle
	d.phasingAcc = make([]float64, WefaxWidth)
	d.phasingN = 0
}

// Feed consumes 8 kHz real audio.
func (d *WefaxDecoder) Feed(x []float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := 2 * math.Pi * wefaxCentre / WefaxRate
	n := len(d.lpTaps)
	for _, s := range x {
		// mix to baseband around 1900 Hz
		c, sn := math.Cos(d.ph), math.Sin(d.ph)
		d.ph += w
		if d.ph > 2*math.Pi {
			d.ph -= 2 * math.Pi
		}
		d.lpI[d.lpPos] = s * c
		d.lpQ[d.lpPos] = -s * sn
		d.lpPos = (d.lpPos + 1) % n
		var I, Q float64
		for k, t := range d.lpTaps {
			j := (d.lpPos + k) % n
			I += t * d.lpI[j]
			Q += t * d.lpQ[j]
		}
		// phase-difference discriminator → instantaneous freq offset
		dph := math.Atan2(Q*d.prevI-I*d.prevQ, I*d.prevI+Q*d.prevQ)
		d.prevI, d.prevQ = I, Q
		hz := dph * WefaxRate / (2 * math.Pi) // −400 black .. +400 white
		v := 0.5 + hz/(2*wefaxDev)
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		d.lumaLP += 0.35 * (v - d.lumaLP)
		d.apt(d.lumaLP)
		d.raster(d.lumaLP)
	}
}

// apt counts black/white transitions in 0.25 s windows: ~300 Hz
// toggling = 150 transitions/window... we use edges/sec instead:
// 300 Hz square → 600 edges/s, 450 Hz → 900 edges/s.
func (d *WefaxDecoder) apt(v float64) {
	b := v > 0.5
	if b != d.aptLast {
		d.aptTrans++
		d.aptLast = b
	}
	d.aptWin++
	const win = WefaxRate / 4 // 0.25 s
	if d.aptWin < win {
		return
	}
	edgesPerSec := float64(d.aptTrans) * 4
	d.aptWin, d.aptTrans = 0, 0
	switch {
	case edgesPerSec > 520 && edgesPerSec < 680: // 300 Hz start
		d.aptStartN++
		d.aptStopN = 0
	case edgesPerSec > 800 && edgesPerSec < 1000: // 450 Hz stop
		if d.aptStopN == 0 {
			// Remember where the stop tone began: every line rastered
			// from here on is tone, not chart. The window that first
			// matched began 0.25 s ago — a line boundary may sit inside
			// it, so count from the line in progress at window start.
			d.stopAtLine = d.lines
			if d.pos < WefaxRate/4 && d.lines > 0 {
				d.stopAtLine = d.lines - 1
			}
		}
		d.aptStopN++
		d.aptStartN = 0
	default:
		d.aptStartN, d.aptStopN = 0, 0
	}
	if d.aptStartN == 8 && d.state != WefaxPhasing { // 2 s of start tone
		// A start tone always begins a fresh chart, whatever free-run
		// was drawing. Keep what came before only if it was a real
		// chart in progress (the start tone itself is not one).
		if d.state == WefaxImage {
			if keep := d.lines - (2*WefaxLPM/60 + 1); keep > 50 {
				d.lines = keep
				d.finish()
			}
		}
		d.reset()
		d.state = WefaxPhasing
	}
	if d.aptStopN == 8 && d.state == WefaxImage { // 2 s of stop tone
		// Drop the stop-tone lines (plus the partial line the tone
		// started in) so the saved chart ends at the real last line.
		// stopAtLine is the line the tone began in; that partial line
		// is mostly chart — keep it, drop everything after.
		if end := d.stopAtLine + 1; end > 0 && end < d.lines {
			d.lines = end
		}
		d.finish()
		d.reset()
	}
}

func (d *WefaxDecoder) raster(v float64) {
	// pos < 0 = a line stretched by a phasing correction: those
	// samples belong to the previous line's tail, not this line.
	x := int(math.Floor(d.pos / d.samplesPerLine * WefaxWidth))
	if x >= 0 && x < WefaxWidth {
		d.line[x] = v
	}
	d.pos++
	if d.pos < d.samplesPerLine {
		return
	}
	d.pos -= d.samplesPerLine
	switch d.state {
	case WefaxPhasing:
		// Phasing lines are black with a short white pulse (~5% of a
		// line) at the line start. Each line: find the pulse and, if it
		// is not already at x=0, move the line clock so the NEXT line
		// starts right after it. A phasing line is recognised by being
		// mostly black; the first line that is not (the chart itself)
		// ends phasing — so none of the phasing tail is drawn.
		const pulse = WefaxWidth * 5 / 100
		mean := 0.0
		for _, p := range d.line {
			mean += p
		}
		mean /= WefaxWidth
		// A phasing line is mostly black (only the ~5% pulse is white).
		// Anything else is either the start-tone tail (≈50% grey) still
		// being sent, or — once real phasing lines have been seen — the
		// chart itself starting.
		best, bi := -1.0, 0
		run := 0.0
		for k := 0; k < pulse; k++ {
			run += d.line[k]
		}
		for i := 0; i < WefaxWidth; i++ {
			if run > best {
				best, bi = run, i
			}
			run += d.line[(i+pulse)%WefaxWidth] - d.line[i]
		}
		// A phasing line is mostly black AND carries a real white
		// pulse. A dark line without one (a chart's black top border)
		// must not steer the clock — searching it for a pulse returns
		// noise and throws the alignment off.
		hasPulse := best/pulse > 0.6
		if mean > 0.25 || !hasPulse {
			if d.phasingN >= 3 {
				d.state = WefaxImage
				d.storeLine() // this line is already chart content
			}
			return
		}
		d.phasingN++
		// The pulse (the sender's line start) was drawn at bi px: our
		// line boundary is bi px EARLY. Delay the next boundary by bi px
		// (negative pos = this line runs longer); bi near W means we are
		// slightly late, so shorten by W−bi instead. Once aligned bi ≈ 0
		// and nothing changes — whatever constant delay the RF chain
		// adds is absorbed here, so the image edge lands at x = 0 too.
		errPx := bi
		if errPx > WefaxWidth/2 {
			errPx -= WefaxWidth
		}
		if errPx < -2 || errPx > 2 {
			d.pos -= float64(errPx) / WefaxWidth * d.samplesPerLine
		}
	default:
		if d.state == WefaxIdle && !d.freeRun {
			return
		}
		if d.state == WefaxIdle {
			d.state = WefaxImage // free-run: start drawing right away
		}
		d.storeLine()
	}
}

// storeLine appends the just-finished line to the image (rolling over
// into a new image when the buffer fills — free-run never stops).
func (d *WefaxDecoder) storeLine() {
	if d.lines >= WefaxMaxLines {
		d.finish()
		d.reset()
		d.state = WefaxImage
	}
	row := d.img.Pix[d.lines*d.img.Stride : d.lines*d.img.Stride+WefaxWidth]
	for i, p := range d.line {
		row[i] = uint8(p*255 + 0.5)
	}
	d.lines++
}

// finish snapshots the received lines as a completed image.
func (d *WefaxDecoder) finish() {
	if d.lines < 10 {
		return
	}
	out := image.NewGray(image.Rect(0, 0, WefaxWidth, d.lines))
	copy(out.Pix, d.img.Pix[:d.lines*d.img.Stride])
	d.done = append(d.done, out)
}

// TakeDone drains images completed by an APT stop / new start.
func (d *WefaxDecoder) TakeDone() []*image.Gray {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.done
	d.done = nil
	return out
}

// Snapshot returns a copy of the image received so far (for manual save).
func (d *WefaxDecoder) Snapshot() *image.Gray {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lines == 0 {
		return nil
	}
	out := image.NewGray(image.Rect(0, 0, WefaxWidth, d.lines))
	copy(out.Pix, d.img.Pix[:d.lines*d.img.Stride])
	return out
}

// Preview renders the most recent rows downscaled into dst (w×h):
// columns averaged across the 1809-wide line, newest line at the bottom.
func (d *WefaxDecoder) Preview(w, h int) (*image.Gray, int, WefaxState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Horizontal: the 1809-wide line is averaged into w columns.
	// Vertical: one preview row per 2 received lines — the true aspect
	// (9 lines/row at w=200) would take minutes to show anything, and
	// checking the decode is about seeing structure arrive NOW. The
	// saved PNG keeps the real aspect.
	step := float64(WefaxWidth) / float64(w)
	const vstep = 2
	rows := d.lines / vstep
	first := 0
	if rows > h {
		first = rows - h
	}
	n := rows - first
	if n < 1 {
		return nil, d.lines, d.state
	}
	// Only the rows that exist: the panel bottom-aligns them, so the
	// newest line always sits at the bottom of the box.
	out := image.NewGray(image.Rect(0, 0, w, n))
	for y := 0; y < n; y++ {
		sl := (first + y) * vstep
		if sl >= d.lines {
			break
		}
		src := d.img.Pix[sl*d.img.Stride : sl*d.img.Stride+WefaxWidth]
		for x := 0; x < w; x++ {
			a := int(float64(x) * step)
			b := int(float64(x+1) * step)
			if b <= a {
				b = a + 1
			}
			s := 0
			for k := a; k < b && k < WefaxWidth; k++ {
				s += int(src[k])
			}
			out.Pix[y*out.Stride+x] = uint8(s / (b - a))
		}
	}
	return out, d.lines, d.state
}

// Clear discards the current image and returns to free-run.
func (d *WefaxDecoder) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reset()
}

// LineOffset nudges the horizontal slant correction / left edge by
// frac of a line (positive = shift image left).
func (d *WefaxDecoder) Shift(frac float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pos += frac * d.samplesPerLine
	for d.pos < 0 {
		d.pos += d.samplesPerLine
	}
	for d.pos >= d.samplesPerLine {
		d.pos -= d.samplesPerLine
	}
}
