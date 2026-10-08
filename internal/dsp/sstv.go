// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"image"
	"image/color"
	"math"
	"sync"
)

// SSTV decoder for the 8 kHz monitor branch. Slow-scan TV carries one
// audio tone per brightness level (1500 Hz black … 2300 Hz white);
// each transmission opens with a 1900 Hz leader and a VIS code that
// names the mode, so "auto" is the protocol's native behaviour.
//
// Supported modes (the ones actually on the air): Martin M1/M2 and
// Scottie S1/S2 — 320×256 sequential RGB. Other VIS codes are
// reported by name but not rastered.

// SSTVMode describes one raster format as a cyclic segment list
// starting at the line sync pulse.
type SSTVMode struct {
	Name     string
	VIS      byte
	Width    int
	Height   int
	PixelSec float64 // one colour pixel duration (family 0)
	Family   int     // 0 cyclic RGB, 1 PD (Y/R/B interlaced), 2 Robot (Y + alternating chroma)
	// segments from line sync, in order; Kind: 0 sync/porch/sep/gap
	// (ignored), 1/2/3 = G/B/R pixel runs (family 0); family 1/2 use
	// the fixed layouts in rasterLinePD/rasterLineRobot.
	Seg []sstvSeg
	// family 1/2 timings (ms)
	SyncMs, BpMs, FpMs, YMs, ChromaMs float64
}

type sstvSeg struct {
	Kind  byte
	DurMs float64
}

func px(ms float64) sstvSeg { return sstvSeg{Kind: 0, DurMs: ms} }
func g(n float64) sstvSeg   { return sstvSeg{Kind: 1, DurMs: n} }
func b(n float64) sstvSeg   { return sstvSeg{Kind: 2, DurMs: n} }
func r(n float64) sstvSeg   { return sstvSeg{Kind: 3, DurMs: n} }

var (
	// Martin M1/M2: sync, porch, G, sep, B, sep, R.
	martinM1 = SSTVMode{Name: "Martin M1", VIS: 44, Width: 320, Height: 256, PixelSec: 0.4576e-3,
		Seg: []sstvSeg{px(4.862), px(0.572), g(146.432), px(0.572), b(146.432), px(0.572), r(146.432)}}
	martinM2 = SSTVMode{Name: "Martin M2", VIS: 40, Width: 320, Height: 256, PixelSec: 0.9152e-3,
		Seg: []sstvSeg{px(4.862), px(0.572), g(292.864), px(0.572), b(292.864), px(0.572), r(292.864)}}
	// Scottie S1/S2: sync sits between blue and red, so the cyclic
	// order from sync is sync, R, G, sep, B, sep.
	scottieS1 = SSTVMode{Name: "Scottie S1", VIS: 60, Width: 320, Height: 256, PixelSec: 0.4576e-3,
		Seg: []sstvSeg{px(9), r(146.432), g(146.432), px(1.5), b(146.432), px(1.5)}}
	scottieS2 = SSTVMode{Name: "Scottie S2", VIS: 56, Width: 320, Height: 256, PixelSec: 0.9152e-3,
		Seg: []sstvSeg{px(9), r(292.864), g(292.864), px(1.5), b(292.864), px(1.5)}}
	// PD family (ISS/ARISS favourite): one line period carries TWO
	// display rows — sync, porch, Y(odd), R, B, Y(even), front porch.
	// G is derived from Y/R/B. 640x496 (PD120/180/240) or 320x256.
	pd90 = SSTVMode{Name: "PD90", VIS: 99, Family: 1, Width: 320, Height: 256,
		SyncMs: 20, BpMs: 2.08, FpMs: 2.30, YMs: 169.68, ChromaMs: 169.68}
	pd120 = SSTVMode{Name: "PD120", VIS: 95, Family: 1, Width: 640, Height: 496,
		SyncMs: 20, BpMs: 2.08, FpMs: 2.30, YMs: 121.03, ChromaMs: 121.03}
	pd180 = SSTVMode{Name: "PD180", VIS: 96, Family: 1, Width: 640, Height: 496,
		SyncMs: 20, BpMs: 2.00, FpMs: 2.30, YMs: 182.50, ChromaMs: 182.50}
	pd240 = SSTVMode{Name: "PD240", VIS: 225, Family: 1, Width: 640, Height: 496,
		SyncMs: 20, BpMs: 2.00, FpMs: 2.30, YMs: 243.94, ChromaMs: 243.94}
	// Robot 36C/72C (the other ISS mode): per line — sync, porch, Y
	// (full row), a 1500/2300 Hz marker tone naming the chroma that
	// follows (R-Y on 1500 lines, B-Y on 2300 lines), gap, chroma row.
	// The missing chroma is held from the previous line that had it.
	robot36 = SSTVMode{Name: "Robot 36C", VIS: 8, Family: 2, Width: 320, Height: 240,
		SyncMs: 9, BpMs: 0.4, FpMs: 0, YMs: 89.072, ChromaMs: 44.536}
	robot72 = SSTVMode{Name: "Robot 72C", VIS: 12, Family: 2, Width: 320, Height: 240,
		SyncMs: 9, BpMs: 0.4, FpMs: 0.4, YMs: 189.48, ChromaMs: 94.74}
)

var sstvVISNames = map[byte]string{
	12: "Robot 72C", 44: "Martin M1", 40: "Martin M2",
	60: "Scottie S1", 56: "Scottie S2", 62: "Scottie S3", 63: "Scottie S4",
	76: "Scottie DX", 45: "Martin M3", 41: "Martin M4",
	95: "PD120", 99: "PD90", 96: "PD180", 225: "PD240", 226: "PD160", 221: "PD50",
	8: "Robot 36C",
}

func sstvModeByVIS(v byte) *SSTVMode {
	switch v {
	case 44:
		return &martinM1
	case 40:
		return &martinM2
	case 60:
		return &scottieS1
	case 56:
		return &scottieS2
	case 99:
		return &pd90
	case 95:
		return &pd120
	case 96:
		return &pd180
	case 225:
		return &pd240
	case 8:
		return &robot36
	case 12:
		return &robot72
	}
	return nil
}

// SSTVDecoder consumes 8 kHz audio and rasters images.
type SSTVDecoder struct {
	mu sync.Mutex

	enabled bool

	// frequency discriminator (mix at 1750 + lowpass + phase deriv)
	ph    float64
	lpI   []float64
	lpQ   []float64
	lpTps []float64
	lpPos int
	prev  complex128
	freq  float64 // smoothed instantaneous tone frequency

	// leader/VIS state machine
	state   int
	run     int    // samples in the current tone class
	runTone int    // tone class of the run (-1 none, 1200, 1900)
	visT0   int64  // sample index of the VIS start bit
	visK    int    // next VIS slot to sample
	visVal  byte   // accumulated bits
	sampleI int64  // global sample counter
	visName string // last seen VIS mode name

	mode  *SSTVMode
	img   *image.NRGBA
	lineY int

	// per-line collection
	collecting bool
	lineBuf    []float64
	lineNeed   int
	lineOff    int   // samples from lineBuf[0] to the true line start
	syncSearch int   // samples left in the first-line sync hunt
	locked     bool  // line timing locked after the first sync
	nextLineAt int64 // sample index of the next line start
	imageStart int64 // sample at which the image state began (VIS stop bit)

	done []*image.NRGBA
	ver  int // bumps on every rastered line (UI caches by it)
	// Robot chroma rows held between lines (R-Y / B-Y arrive on
	// alternating lines)
	lastRY, lastBY []float64
}

const (
	sstvHunt = iota
	sstvBreak1
	sstvLeader2
	sstvBreak2
	sstvVIS
	sstvImage
)

func NewSSTVDecoder() *SSTVDecoder {
	d := &SSTVDecoder{}
	d.lpTps = DesignLowpass(31, 900, 8000)
	d.lpI = make([]float64, len(d.lpTps))
	d.lpQ = make([]float64, len(d.lpTps))
	return d
}

func (d *SSTVDecoder) SetEnabled(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.enabled = on
	if on {
		d.state = sstvHunt
		d.run, d.runTone = 0, -1
	}
}
func (d *SSTVDecoder) Enabled() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.enabled }

// Feed consumes one block of 8 kHz monitor audio.
func (d *SSTVDecoder) Feed(x []float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return
	}
	n := len(d.lpTps)
	for _, s := range x {
		d.ph += 2 * math.Pi * 1750 / 8000
		if d.ph > 2*math.Pi {
			d.ph -= 2 * math.Pi
		}
		d.lpI[d.lpPos] = s * math.Cos(d.ph)
		d.lpQ[d.lpPos] = -s * math.Sin(d.ph)
		d.lpPos = (d.lpPos + 1) % n
		var I, Q float64
		for k, t := range d.lpTps {
			j := (d.lpPos + k) % n
			I += t * d.lpI[j]
			Q += t * d.lpQ[j]
		}
		z := complex(I, Q)
		if d.prev != 0 {
			prod := z * complex(real(d.prev), -imag(d.prev))
			f := 1750 + math.Atan2(imag(prod), real(prod))*8000/(2*math.Pi)
			d.freq += 0.4 * (f - d.freq)
		}
		d.prev = z
		d.sampleI++
		d.step()
	}
}

// tone class of the current smoothed frequency.
func (d *SSTVDecoder) tone() int {
	switch {
	case math.Abs(d.freq-1900) < 110:
		return 1900
	case d.freq < 1400:
		return 1200
	}
	return 0
}

func (d *SSTVDecoder) step() {
	t := d.tone()
	if t == d.runTone {
		d.run++
	} else {
		d.runTone, d.run = t, 1
	}
	switch d.state {
	case sstvHunt:
		if d.runTone == 1900 && d.run >= 1600 { // 200 ms leader
			d.state = sstvBreak1
		}
	case sstvBreak1:
		if d.runTone == 1200 && d.run >= 40 {
			d.state = sstvLeader2
		} else if d.runTone == 1900 {
			// still leader, fine
		} else if d.run > 400 {
			d.state = sstvHunt
		}
	case sstvLeader2:
		if d.runTone == 1900 && d.run >= 1600 {
			d.state = sstvBreak2
		} else if d.runTone != 1200 && d.runTone != 1900 && d.run > 600 {
			d.state = sstvHunt
		}
	case sstvBreak2:
		// the 30 ms break before VIS is the start bit; its beginning
		// is run start = sampleI - run + 1.
		if d.runTone == 1200 && d.run >= 160 {
			d.visT0 = d.sampleI - int64(d.run) + 1
			d.visK, d.visVal = 0, 0
			d.state = sstvVIS
		} else if d.runTone != 1900 && d.run > 600 {
			d.state = sstvHunt
		}
	case sstvVIS:
		for d.visK < 10 && d.sampleI >= d.visT0+int64(d.visK)*240+120 {
			bit := byte(0)
			if d.freq > 1250 {
				bit = 1
			}
			switch d.visK {
			case 0:
				if bit != 0 { // start bit is 1200 Hz
					d.state = sstvHunt
					return
				}
			case 8: // even parity — accept either, report only
			case 9:
				if bit != 1 {
					d.state = sstvHunt
					return
				}
				d.beginImage()
				return
			default:
				if bit == 1 {
					d.visVal |= 1 << (d.visK - 1)
				}
			}
			d.visK++
		}
	case sstvImage:
		d.stepImage()
	}
}

func (d *SSTVDecoder) beginImage() {
	name, ok := sstvVISNames[d.visVal]
	if !ok {
		name = "VIS " + string(rune('0'+d.visVal/10)) + string(rune('0'+d.visVal%10))
	}
	d.visName = name
	d.mode = sstvModeByVIS(d.visVal)
	if d.mode == nil {
		d.state = sstvHunt // known name, unsupported raster
		return
	}
	d.img = image.NewNRGBA(image.Rect(0, 0, d.mode.Width, d.mode.Height))
	d.lineY = 0
	d.state = sstvImage
	d.imageStart = d.sampleI
	d.collecting = false
	d.locked = false
	d.lineOff = 0
	d.syncSearch = 8000 // 1 s to find the first line sync
}

func (d *SSTVDecoder) lineSamples() int {
	return int(d.lineMs() * 8)
}

// lineMs is one collection period in ms: one row for family 0, two
// rows for PD and Robot.
func (d *SSTVDecoder) lineMs() float64 {
	m := d.mode
	switch m.Family {
	case 1:
		// two rows: sync+porch+Y,R,B,Y+front-porch each
		return m.SyncMs*2 + m.BpMs*2 + m.FpMs*2 + m.YMs*8
	case 2:
		// two rows: sync+porch+Y+marker/gap+chroma each
		return m.SyncMs*2 + m.BpMs*2 + m.YMs*2 + m.ChromaMs*2 + m.blankMs()*2
	}
	tot := 0.0
	for _, sg := range m.Seg {
		tot += sg.DurMs
	}
	return tot
}

func (m *SSTVMode) blankMs() float64 {
	if m.Name == "Robot 72C" {
		return 6.0
	}
	return 7.0
}

func (d *SSTVDecoder) stepImage() {
	if d.collecting {
		d.lineBuf = append(d.lineBuf, d.freq)
		if len(d.lineBuf) >= d.lineNeed+d.lineOffGuard() {
			d.rasterLine()
			d.collecting = false
			if d.mode.Family == 0 {
				d.lineY++
			} else {
				d.lineY += 2
			}
			if d.lineY >= d.mode.Height {
				d.finishImage()
				return
			}
			// Line-locked: the next line starts exactly one line
			// period after this one.
			d.locked = true
			d.nextLineAt += int64(d.lineNeed)
			return
		}
		return
	}
	if d.runTone == 1900 && d.run >= 1600 {
		// a new transmission started: keep what we have
		d.finishImage()
		return
	}
	if d.locked {
		const guard = 20
		if d.sampleI >= d.nextLineAt-int64(guard) {
			d.collecting = true
			d.lineOff = guard
			d.lineBuf = d.lineBuf[:0]
			return
		}
		if d.sampleI > d.nextLineAt+400 {
			d.finishImage() // line timing lost
		}
		return
	}
	// First line: hunt the sync — a 1200 Hz run of at least 60% of the
	// mode's sync segment.
	syncNeed := int(d.mode.SyncMs*8*0.6) + 8
	// The VIS stop bit is itself 30 ms of 1200 Hz — longer than any
	// line sync — so the first line sync is the 1200 run that STARTS
	// after we entered the image state.
	if d.runTone == 1200 && d.run >= syncNeed && d.sampleI-int64(d.run)+1 > d.imageStart+60 {
		lineStart := d.sampleI - int64(d.run) + 1
		d.collecting = true
		d.lineNeed = d.lineSamples()
		d.lineOff = -d.run // line start precedes lineBuf[0]
		d.lineBuf = d.lineBuf[:0]
		d.nextLineAt = lineStart + int64(d.lineNeed)
		return
	}
	if d.syncSearch > 0 {
		d.syncSearch--
	} else {
		d.finishImage() // sync never found
	}
}

// lineOffGuard is how many extra samples a collected line runs past
// lineNeed when the buffer started late (first line).
func (d *SSTVDecoder) lineOffGuard() int {
	if d.lineOff < 0 {
		return -d.lineOff
	}
	return 0
}

// rasterLine decodes the collected per-line frequency trace into the
// current image row.
func (d *SSTVDecoder) rasterLine() {
	d.ver++
	m := d.mode
	switch m.Family {
	case 1:
		d.rasterLinePD()
		return
	case 2:
		d.rasterLineRobot()
		return
	}
	pt := m.PixelSec * 8000 // samples per colour pixel
	pos := float64(d.lineOff)
	var chans [3][]float64
	for _, sg := range m.Seg {
		n := int(sg.DurMs * 8)
		if sg.Kind >= 1 {
			ch := sg.Kind - 1
			// average the middle 70% of each pixel window
			for p := 0; p < m.Width; p++ {
				a := int(pos + pt*float64(p) + 0.15*pt)
				b := int(pos + pt*float64(p+1) - 0.15*pt)
				if b <= a {
					b = a + 1
				}
				if b > len(d.lineBuf) {
					b = len(d.lineBuf)
				}
				if a < 0 {
					a = 0
				}
				if a >= b {
					chans[ch] = append(chans[ch], 0)
					continue
				}
				var sum float64
				for i := a; i < b; i++ {
					sum += d.lineBuf[i]
				}
				chans[ch] = append(chans[ch], sum/float64(b-a))
			}
		}
		pos += float64(n)
	}
	lvl := func(f float64) uint8 {
		v := (f - 1500) / 800 * 255
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v)
	}
	y := d.lineY
	for x := 0; x < m.Width; x++ {
		gv := pick(chans[0], x)
		bv := pick(chans[1], x)
		rv := pick(chans[2], x)
		d.img.SetNRGBA(x, y, color.NRGBA{lvl(rv), lvl(gv), lvl(bv), 255})
	}
}

// avgWin averages the middle 70% of one pixel window in the line buffer.
func (d *SSTVDecoder) avgWin(from, pixSamples float64, p, width int) float64 {
	a := int(from + pixSamples*float64(p) + 0.15*pixSamples)
	b := int(from + pixSamples*float64(p+1) - 0.15*pixSamples)
	if b <= a {
		b = a + 1
	}
	if a < 0 {
		a = 0
	}
	if b > len(d.lineBuf) {
		b = len(d.lineBuf)
	}
	if a >= b {
		return 1500
	}
	var sum float64
	for i := a; i < b; i++ {
		sum += d.lineBuf[i]
	}
	return sum / float64(b-a)
}

func (d *SSTVDecoder) lvl(f float64) float64 {
	v := (f - 1500) / 800 * 255
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return v
}

// rasterLinePD decodes one PD line period into two rows: sync, porch,
// Y(odd), R, B, Y(even), front porch — repeated twice. G is recovered
// from Y/R/B.
func (d *SSTVDecoder) rasterLinePD() {
	m := d.mode
	w := m.Width
	pt := m.YMs * 8 / float64(w) // samples per pixel
	pos := float64(d.lineOff) + m.SyncMs*8 + m.BpMs*8
	seg := func() []float64 {
		out := make([]float64, w)
		for p := 0; p < w; p++ {
			out[p] = d.avgWin(pos, pt, p, w)
		}
		pos += pt * float64(w)
		return out
	}
	y1 := seg()
	rr := seg()
	bb := seg()
	y2 := seg()
	put := func(y int, Y, R, B []float64) {
		if y >= m.Height {
			return
		}
		for x := 0; x < w; x++ {
			yv, rv, bv := d.lvl(Y[x]), d.lvl(R[x]), d.lvl(B[x])
			gv := 1.7035*yv - 0.5094*rv - 0.1942*bv
			if gv < 0 {
				gv = 0
			}
			if gv > 255 {
				gv = 255
			}
			d.img.SetNRGBA(x, y, color.NRGBA{uint8(rv), uint8(gv), uint8(bv), 255})
		}
	}
	put(d.lineY, y1, rr, bb)
	// second row: sync, porch again then Y(even) with the same R/B
	pos += m.FpMs*8 + m.SyncMs*8 + m.BpMs*8
	y2b := make([]float64, w)
	for p := 0; p < w; p++ {
		y2b[p] = d.avgWin(pos, pt, p, w)
	}
	_ = y2
	put(d.lineY+1, y2b, rr, bb)
}

// rasterLineRobot decodes one Robot 36C/72C line period (two rows):
// sync, porch, Y, marker tone (1500 = R-Y follows, 2300 = B-Y), gap,
// chroma; then the same again for the second row. The chroma not
// carried on a line is held from the last line that had it.
func (d *SSTVDecoder) rasterLineRobot() {
	m := d.mode
	w := m.Width
	ptY := m.YMs * 8 / float64(w)
	ptC := m.ChromaMs * 8 / float64(w)
	gap := m.blankMs() * 8 / 3
	pos := float64(d.lineOff)
	for half := 0; half < 2; half++ {
		pos += m.SyncMs*8 + m.BpMs*8
		Y := make([]float64, w)
		for p := 0; p < w; p++ {
			Y[p] = d.avgWin(pos, ptY, p, w)
		}
		pos += ptY * float64(w)
		// marker tone: average the middle of the 2/3-blank window
		mk := 0.0
		ma := int(pos + gap*0.3)
		mb := int(pos + gap*1.6)
		if mb > len(d.lineBuf) {
			mb = len(d.lineBuf)
		}
		if ma < mb {
			for i := ma; i < mb; i++ {
				mk += d.lineBuf[i]
			}
			mk /= float64(mb - ma)
		}
		pos += 2 * gap
		C := make([]float64, w)
		for p := 0; p < w; p++ {
			C[p] = d.avgWin(pos, ptC, p, w)
		}
		pos += ptC * float64(w)
		isRY := mk < 1900
		if isRY {
			d.lastRY = C
		} else {
			d.lastBY = C
		}
		ry, by := d.lastRY, d.lastBY
		y := d.lineY + half
		if y < m.Height && ry != nil && by != nil {
			for x := 0; x < w; x++ {
				yv := d.lvl(Y[x])
				rv := yv + (d.lvl(pick(ry, x))-128)*2
				bv := yv + (d.lvl(pick(by, x))-128)*2
				gv := yv - 0.509*((d.lvl(pick(ry, x))-128)*2) - 0.194*((d.lvl(pick(by, x))-128)*2)
				cl := func(v float64) uint8 {
					if v < 0 {
						v = 0
					}
					if v > 255 {
						v = 255
					}
					return uint8(v)
				}
				d.img.SetNRGBA(x, y, color.NRGBA{cl(rv), cl(gv), cl(bv), 255})
			}
		}
	}
}

func pick(a []float64, i int) float64 {
	if i < len(a) {
		return a[i]
	}
	return 1500
}

func (d *SSTVDecoder) finishImage() {
	if d.img != nil && d.lineY > 4 {
		cp := image.NewNRGBA(d.img.Bounds())
		copy(cp.Pix, d.img.Pix)
		d.done = append(d.done, cp)
		if len(d.done) > 4 {
			d.done = d.done[len(d.done)-4:]
		}
	}
	d.img, d.mode, d.lineY = nil, nil, 0
	d.collecting = false
	d.state = sstvHunt
	d.run, d.runTone = 0, -1
}

// TakeDone drains finished images (newest last).
func (d *SSTVDecoder) TakeDone() []*image.NRGBA {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.done
	d.done = nil
	return out
}

// Snapshot returns the in-progress image and progress info. The
// version only changes when a line was rastered, so callers can skip
// re-copying while nothing happens.
func (d *SSTVDecoder) Snapshot(ver int) (*image.NRGBA, string, int, int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.img == nil || ver == d.ver {
		return nil, d.visName, d.lineY, d.modeH(), d.ver
	}
	cp := image.NewNRGBA(d.img.Bounds())
	copy(cp.Pix, d.img.Pix)
	return cp, d.visName, d.lineY, d.modeH(), d.ver
}

func (d *SSTVDecoder) modeH() int {
	if d.mode == nil {
		return 0
	}
	return d.mode.Height
}

// Clear drops everything in progress and finished.
func (d *SSTVDecoder) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.done = nil
	d.img, d.mode, d.lineY = nil, nil, 0
	d.state = sstvHunt
	d.run, d.runTone = 0, -1
	d.visName = ""
}
