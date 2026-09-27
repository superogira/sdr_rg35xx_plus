// RTTY decoder: amateur Baudot radioteletype, 45.45 baud / 170 Hz
// shift, mark 2125 Hz / space 2295 Hz (the universal ham convention in
// a USB receiver). Demodulation mixes the 8 kHz monitor audio to
// baseband at the shift centre (2210 Hz), low-pass decimates to 2 kHz
// and takes the phase derivative: mark sits at −85 Hz, space at +85,
// so the sign of the integrated instantaneous frequency is the bit.
// Characters are framed asynchronously (start bit, 5 data bits LSB
// first, 1.5 stop bits) exactly like a teleprinter, with ITA2
// letters/figures shifting and USOS (shift back to letters on space).
package dsp

import (
	"math"
	"strings"
	"sync"
)

const (
	rttyMarkHz   = 2125.0
	rttySpaceHz  = 2295.0
	rttyCentreHz = (rttyMarkHz + rttySpaceHz) / 2 // 2210
	rttyBaud     = 45.4545
	// The monitor branch feeds 8 kHz; after ÷4 decimation the demod
	// stream is 2 kHz, where one bit is 44 samples and a quarter-bit
	// integration window is 11 samples.
	rttyFeedRate  = 8000
	rttyDemodRate = 2000
	rttyBitLen    = 44
	rttyQuarter   = rttyBitLen / 4
)

// ITA2 (Baudot-Murray) code tables, letters and figures. Control
// characters (NUL/BEL/WRU) decode to "" — they carry no printable
// text; LTRS/FIGS are handled by the framer itself.
var rttyLetters = [32]string{
	"", "E", "\n", "A", " ", "S", "I", "U",
	"\r", "D", "R", "J", "N", "F", "C", "K",
	"T", "Z", "L", "W", "H", "Y", "P", "Q",
	"O", "B", "G", "\x1b", "M", "X", "V", "\x1f",
}
var rttyFigures = [32]string{
	"", "3", "\n", "-", " ", "'", "8", "7",
	"\r", "", "4", "", ",", "!", ":", "(",
	"5", "\"", ")", "2", "#", "6", "0", "1",
	"9", "?", "&", "\x1b", ".", "/", ";", "\x1f",
}

// rttyDebug enables framer tracing in tests.
var rttyDebug func(format string, args ...any)

// RTTYDecoder turns 8 kHz real audio into decoded text lines.
type RTTYDecoder struct {
	mu      sync.Mutex
	enabled bool
	rev     bool // mark/space swapped (LSB-side or reversed signals)
	usos    bool

	mixPhase float64 // LO phase for the centre-frequency mixer
	mixTaps  []float64
	mixHist  []complex128

	// 2 kHz demod state
	prev   complex128
	qSum   float64 // integrated instantaneous frequency over the window
	qCount int

	// bit stream + asynchronous character framing (quarter-bit grid)
	qTotal    int // absolute quarter counter (debug/timing)
	lastBit   int // previous quarter decision, mark = 1
	collect   int // quarters until the next sampling point (0 = sample now)
	sampling  bool
	bits      uint8
	nBits     int
	shiftFigs bool
	cur       strings.Builder
	lines     []string

	markLvl, spaceLvl float64 // smoothed tone levels for the tuning bar
}

// NewRTTYDecoder builds the decoder with USOS on.
func NewRTTYDecoder() *RTTYDecoder {
	d := &RTTYDecoder{usos: true}
	d.mixTaps = DesignLowpass(127, 300, rttyFeedRate)
	return d
}

func (d *RTTYDecoder) Enabled() bool       { return d.enabled }
func (d *RTTYDecoder) SetEnabled(on bool)  { d.enabled = on }
func (d *RTTYDecoder) Reversed() bool      { return d.rev }
func (d *RTTYDecoder) SetReversed(on bool) { d.rev = on }

// Feed consumes one block of 8 kHz real audio.
func (d *RTTYDecoder) Feed(x []float64) {
	if !d.enabled || len(x) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// Mix to baseband at the shift centre and low-pass decimate to
	// 2 kHz (one complex output per 4 input samples).
	mixed := make([]complex128, len(x))
	for i, v := range x {
		d.mixPhase += 2 * math.Pi * rttyCentreHz / rttyFeedRate
		mixed[i] = complex(v*math.Cos(d.mixPhase), -v*math.Sin(d.mixPhase))
	}
	var dec []complex128
	complexFIRDecim(d.mixTaps, &d.mixHist, 4, mixed, &dec)

	for _, z := range dec {
		// Instantaneous frequency (Hz) from the phase derivative.
		prod := z * conj(d.prev)
		d.prev = z
		f := math.Atan2(imag(prod), real(prod)) * rttyDemodRate / (2 * math.Pi)

		d.qSum += f
		d.qCount++
		if d.qCount < rttyQuarter {
			continue
		}

		bit := 0
		if d.qSum < 0 { // negative = below centre = mark
			bit = 1
		}
		if d.rev {
			bit ^= 1
		}
		mag := math.Abs(d.qSum) / (85.0 * rttyQuarter)
		if bit == 1 {
			d.markLvl += 0.25 * (mag - d.markLvl)
			d.spaceLvl += 0.25 * (0 - d.spaceLvl)
		} else {
			d.spaceLvl += 0.25 * (mag - d.spaceLvl)
			d.markLvl += 0.25 * (0 - d.markLvl)
		}
		d.qSum, d.qCount = 0, 0
		d.qTotal++
		d.stepBit(bit)
	}
}

// stepBit advances the asynchronous character framer by one
// quarter-bit decision. Data bit k (k = 0..4) is sampled at its centre:
// start edge + (1.5 + k) bits = edge + 6 + 4k quarters; the stop bit
// centre is edge + 30 quarters. Any per-character clock error is
// bounded — every character restarts the hunt.
func (d *RTTYDecoder) stepBit(bit int) {
	defer func() { d.lastBit = bit }()

	if !d.sampling {
		// Hunt: a mark→space edge while idle is a start bit.
		if d.lastBit == 1 && bit == 0 {
			d.sampling = true
			d.bits, d.nBits = 0, 0
			// Data bit k is sampled at edge + 6 + 4k quarters (its
			// centre; the detected edge already carries the ~2-quarter
			// filter/window lag, which is systematic and cancels out).
			d.collect = 6
		}
		return
	}
	d.collect--
	if d.collect > 0 {
		return
	}
	// This quarter IS a sampling point (bit centre).
	if d.nBits < 5 {
		if bit == 1 {
			d.bits |= 1 << d.nBits
		}
		d.nBits++
		if d.nBits == 5 {
			// Stop sample sits 6 quarters past the 5th data bit — the
			// centre of the 1.5 stop bits with margin from both edges;
			// at +8 the sample hit the NEXT start bit exactly and every
			// character was discarded.
			d.collect = 6
		} else {
			d.collect = 4
		}
		return
	}
	// Stop bit sample: must be mark.
	if bit == 1 {
		d.decode(d.bits)
	}
	d.sampling = false
}

// decode maps a 5-bit value through the shift state, applying
// LTRS/FIGS shifts, USOS and line breaks.
func (d *RTTYDecoder) decode(bits uint8) {
	switch bits {
	case 0x1f:
		d.shiftFigs = false
		return
	case 0x1b:
		d.shiftFigs = true
		return
	}
	s := rttyLetters[bits]
	if d.shiftFigs {
		s = rttyFigures[bits]
	}
	// USOS: a space always returns to letters.
	if d.usos && bits == 0x04 {
		d.shiftFigs = false
	}
	switch s {
	case "":
		return
	case "\r":
		line := d.cur.String()
		d.cur.Reset()
		d.lines = append(d.lines, line)
		if len(d.lines) > 200 {
			d.lines = d.lines[len(d.lines)-200:]
		}
		return
	case "\n":
		return
	}
	d.cur.WriteString(s)
}

// TakeLines returns decoded complete lines and clears the buffer.
func (d *RTTYDecoder) TakeLines() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.lines
	d.lines = nil
	return out
}

// Current returns the partially-received line (for a live display).
func (d *RTTYDecoder) Current() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur.String()
}

// Levels returns smoothed mark/space tuning levels (0..~1).
func (d *RTTYDecoder) Levels() (mark, space float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.markLvl, d.spaceLvl
}
