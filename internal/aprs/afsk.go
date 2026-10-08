// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import (
	"math"
	"sync"
)

// Bell 202 AFSK: 1200 baud, mark (1) = 1200 Hz, space (0) = 2200 Hz,
// NRZI (0 = tone change), HDLC framing with bit stuffing after five
// consecutive ones, LSB-first bit order on air.

const (
	MarkHz  = 1200
	SpaceHz = 2200
	Baud    = 1200
	TXRate  = 48000
	// RXRate is the monitor branch rate the demodulator is fed at.
	RXRate = 8000
	// mixCentre is the discriminator centre between the two tones.
	mixCentre = (MarkHz + SpaceHz) / 2
)

// Modulate renders a frame body (dest..info) as beacon audio at TXRate:
// preamble flags (VOX open time), FCS, bit stuffing, tail flag — all
// phase-continuous so nothing clicks.
func Modulate(body []byte, amp float64, preambleFlags int) []float64 {
	// Bit assembly: flags are sent unstuffed; the frame body (with its
	// FCS) is stuffed after every run of five ones.
	fcsLo, fcsHi := FCSBytes(body)
	stuffed := body
	stuffed = append(stuffed, fcsLo, fcsHi)

	bits := make([]byte, 0, (preambleFlags+2)*8+len(stuffed)*9)
	pushFlag := func() {
		f := byte(0x7E)
		for i := 0; i < 8; i++ {
			bits = append(bits, (f>>i)&1)
		}
	}
	for i := 0; i < preambleFlags; i++ {
		pushFlag()
	}
	ones := 0
	for _, b := range stuffed {
		for i := 0; i < 8; i++ {
			bit := (b >> i) & 1
			bits = append(bits, bit)
			if bit == 1 {
				ones++
				if ones == 5 {
					bits = append(bits, 0) // stuffed zero
					ones = 0
				}
			} else {
				ones = 0
			}
		}
	}
	pushFlag()

	// NRZI + continuous-phase FSK, with a 5 ms fade at both ends so the
	// speaker (and any AGC downstream) is not kicked by a hard onset.
	spb := float64(TXRate) / Baud
	phase := 0.0
	tone := float64(MarkHz)
	out := make([]float64, 0, int(float64(len(bits))*spb)+8)
	for _, bit := range bits {
		if bit == 0 {
			if tone == MarkHz {
				tone = SpaceHz
			} else {
				tone = MarkHz
			}
		}
		inc := 2 * math.Pi * tone / TXRate
		n := int(spb)
		for i := 0; i < n; i++ {
			phase += inc
			out = append(out, amp*math.Sin(phase))
		}
		rem := spb - float64(n)
		if rem > 0.5 {
			phase += inc
			out = append(out, amp*math.Sin(phase))
		}
	}
	// Fade IN only (5 ms): protects the VOX onset and any downstream
	// AGC from a hard kick. There must be NO fade-out — the closing
	// flag has to leave at full amplitude or receivers cannot close
	// the frame (this is exactly what broke APRSdroid). A 100 ms
	// silent tail lets the speaker and any codec settle instead.
	const fade = 240 // 5 ms at 48 kHz
	for i := 0; i < fade && i < len(out)/4; i++ {
		out[i] *= float64(i) / float64(fade)
	}
	out = append(out, make([]float64, 4800)...)
	return out
}

// Demodulator consumes monitor-branch audio at RXRate and yields
// FCS-validated frame bodies via TakeFrames.
//
// Discriminator: two complex correlators (mark 1200 / space 2200 Hz)
// over an 8-sample boxcar — exactly one bit at 8 kHz, which both
// preserves single-bit NRZI excursions (a longer FIR smears them
// away) and places a perfect null on the opposite tone (they differ
// by 1 kHz = one full beat across the window).
type Demodulator struct {
	mu sync.Mutex

	ring  [2][8]complex128
	wpos  int
	sum   [2]complex128
	ph    [2]float64
	level float64

	soft float64 // lightly smoothed normalised mark−space
	// bit clock + NRZI
	phase    float64
	lastRawT int  // glitch-filtered tone of the current bit
	haveRaw  bool // any confident tone seen yet
	lastT    int  // last sampled tone decision (for NRZI)
	haveT    bool

	// HDLC
	shift  uint8
	onesR  int
	inFlag bool
	frame  []byte
	cur    byte
	curN   int
	stuff1 int
	frames [][]byte
}

// NewDemodulator builds one.
func NewDemodulator() *Demodulator { return &Demodulator{} }

// Feed processes one block of monitor audio.
func (d *Demodulator) Feed(x []float64) {
	if len(x) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, v := range x {
		var mag [2]float64
		for t := 0; t < 2; t++ {
			f := float64(MarkHz)
			if t == 1 {
				f = SpaceHz
			}
			d.ph[t] += 2 * math.Pi * f / RXRate
			if d.ph[t] > 2*math.Pi {
				d.ph[t] -= 2 * math.Pi
			}
			z := complex(v*math.Cos(d.ph[t]), -v*math.Sin(d.ph[t]))
			d.sum[t] += z - d.ring[t][d.wpos]
			d.ring[t][d.wpos] = z
			mag[t] = math.Hypot(real(d.sum[t]), imag(d.sum[t]))
		}
		d.wpos = (d.wpos + 1) % 8
		tot := mag[0] + mag[1]
		d.level += 0.02 * (tot - d.level)
		if tot < 1e-9 {
			continue
		}
		n := (mag[0] - mag[1]) / tot // −1 (space) .. +1 (mark)
		d.soft += 0.5 * (n - d.soft)
		if math.Abs(d.soft) >= 0.25 {
			t := 1 // mark
			if d.soft < 0 {
				t = 0 // space
			}
			if !d.haveRaw {
				d.lastRawT, d.haveRaw = t, true
			} else if t != d.lastRawT {
				// NRZI transition = a bit boundary just passed: pull
				// the sampling clock toward the middle of the new bit
				// (light DPLL; edge detection lags ~1 sample).
				d.phase += 0.35 * (0.25 - d.phase)
				d.lastRawT = t
			}
		}
		d.phase += float64(Baud) / float64(RXRate)
		if d.phase < 1 {
			continue
		}
		d.phase -= 1
		if !d.haveRaw {
			continue
		}
		// Sample the glitch-filtered tone at mid-bit; NRZI: a tone
		// CHANGE is a 0 bit.
		bitTone := d.lastRawT
		var bit byte
		if d.haveT && bitTone == d.lastT {
			bit = 1
		}
		d.lastT, d.haveT = bitTone, true
		d.stepBit(bit)
	}
}

// stepBit advances the HDLC deframer by one decoded bit.
func (d *Demodulator) stepBit(bit byte) {
	d.shift = d.shift<<1 | bit
	if bit == 1 {
		d.onesR++
	} else {
		d.onesR = 0
	}
	if d.shift == 0x7E {
		d.closeFrame()
		d.inFlag = true
		return
	}
	if d.onesR == 6 {
		// Six raw ones cannot be data (stuffing caps runs at five) —
		// they are the middle of a flag. The frame, if any, ended at
		// its last complete byte; the partial current byte is flag
		// bits and was never appended.
		d.closeFrame()
		d.inFlag = true
		return
	}
	if d.onesR > 6 {
		d.inFlag = false // seven or more ones: a real abort sequence
		return
	}
	if !d.inFlag {
		return
	}
	// De-stuff: a 0 after exactly five ones is stuffing, not data.
	if d.stuff1 == 5 && bit == 0 {
		d.stuff1 = 0
		return
	}
	if bit == 1 {
		d.stuff1++
	} else {
		d.stuff1 = 0
	}
	d.cur |= bit << d.curN
	d.curN++
	if d.curN == 8 {
		d.frame = append(d.frame, d.cur)
		d.cur, d.curN = 0, 0
		if len(d.frame) > 330 {
			d.inFlag = false
			d.frame = d.frame[:0]
		}
	}
}

// closeFrame validates and emits the collected frame, then resets the
// collector for the next one.
func (d *Demodulator) closeFrame() {
	if d.inFlag && len(d.frame) >= 17 && ValidFCS(d.frame) {
		body := append([]byte(nil), d.frame[:len(d.frame)-2]...)
		if len(d.frames) > 16 {
			d.frames = d.frames[1:]
		}
		d.frames = append(d.frames, body)
	}
	d.frame = d.frame[:0]
	d.cur, d.curN = 0, 0
	d.stuff1 = 0
}

// TakeFrames returns and clears validated frame bodies.
func (d *Demodulator) TakeFrames() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.frames
	d.frames = nil
	return f
}

// Levels reports the smoothed discriminator level (for a tuning aid).
func (d *Demodulator) Level() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.level
}
