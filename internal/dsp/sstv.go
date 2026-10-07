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
	PixelSec float64 // one colour pixel duration
	// segments from line sync, in order; Kind: 0 sync/porch/sep
	// (ignored), 1/2/3 = G/B/R pixel runs.
	Seg []sstvSeg
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
	scottieS2 = SSTVMode{Name: "Scottie S2", VIS: 61, Width: 320, Height: 256, PixelSec: 0.9152e-3,
		Seg: []sstvSeg{px(9), r(292.864), g(292.864), px(1.5), b(292.864), px(1.5)}}
)

var sstvVISNames = map[byte]string{
	8: "Robot 36C", 12: "Robot 72C", 44: "Martin M1", 40: "Martin M2",
	60: "Scottie S1", 61: "Scottie S2", 62: "Scottie S3", 63: "Scottie S4",
	76: "Scottie DX", 45: "Martin M3", 41: "Martin M4", 95: "PD120", 99: "PD180",
}

func sstvModeByVIS(v byte) *SSTVMode {
	switch v {
	case 44:
		return &martinM1
	case 40:
		return &martinM2
	case 60:
		return &scottieS1
	case 61:
		return &scottieS2
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
	tot := 0.0
	for _, sg := range d.mode.Seg {
		tot += sg.DurMs
	}
	return int(tot * 8)
}

func (d *SSTVDecoder) stepImage() {
	if d.collecting {
		d.lineBuf = append(d.lineBuf, d.freq)
		if len(d.lineBuf) >= d.lineNeed+d.lineOffGuard() {
			d.rasterLine()
			d.collecting = false
			d.lineY++
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
	syncNeed := int(d.mode.Seg[0].DurMs*8*0.6) + 8
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
