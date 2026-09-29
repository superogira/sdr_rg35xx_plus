// Mode S demodulation from raw rtl_tcp IQ: envelope detection,
// preamble matching, PPM bit sampling and CRC-24 validation, feeding
// the same DF17/DF18 decoder the Beast path uses. Designed for
// capture rates ≥ ~2 MSPS (positions are computed in microseconds so
// no bit-drift accumulates at any rate).
package adsb

import (
	"fmt"

	"math"
	"sync"
)

// crc24 is the Mode S checksum (poly 0xFFF409) over one-bit-per-byte
// slices as produced by bytesToBits.
func crc24(bits []byte) uint32 {
	crc := uint32(0)
	for _, b := range bits {
		bit := uint32(b & 1)
		if (crc>>23)&1 != bit {
			crc = ((crc << 1) ^ 0xFFF409) & 0xFFFFFF
		} else {
			crc = (crc << 1) & 0xFFFFFF
		}
	}
	return crc
}

// bytesToBits MSB-first expansion.
func bytesToBits(b []byte) []byte {
	out := make([]byte, 0, len(b)*8)
	for _, v := range b {
		for m := byte(0x80); m != 0; m >>= 1 {
			if v&m != 0 {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		}
	}
	return out
}

// bitsToBytes packs MSB-first.
func bitsToBytes(bits []byte) []byte {
	out := make([]byte, 0, (len(bits)+7)/8)
	for i := 0; i+8 <= len(bits); i += 8 {
		var v byte
		for k := 0; k < 8; k++ {
			v = (v << 1) | bits[i+k]
		}
		out = append(out, v)
	}
	return out
}

// appendCRC24 computes the checksum of the data bits and appends its
// 24 bits (message assembler for tests / transmissions).
func appendCRC24(data []byte) []byte {
	bits := bytesToBits(data)
	c := crc24(bits)
	extra := make([]byte, 3)
	extra[0] = byte(c >> 16)
	extra[1] = byte(c >> 8)
	extra[2] = byte(c)
	return append(append([]byte{}, data...), extra...)
}

// modesDebug hooks demod tracing in tests.
var modesDebug func(f string, a ...any)

// ModeSDemod turns raw u8 IQ at a known rate into Mode S messages.
type ModeSDemod struct {
	mu    sync.Mutex
	rate  float64 // samples per second
	store *Store

	mag    []float64 // ring buffer of magnitudes
	head   int
	filled int

	noise float64 // rolling noise-floor estimate
	count int

	// Diagnostics.
	Preambles int
	CrcFails  int
	Decoded   int
}

func NewModeSDemod(rate float64, store *Store) *ModeSDemod {
	return &ModeSDemod{rate: rate, store: store, mag: make([]float64, 4096), noise: 20}
}

// FeedIQ consumes one block of interleaved u8 IQ.
func (d *ModeSDemod) FeedIQ(buf []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(buf) / 2
	for i := 0; i < n; i++ {
		re := float64(buf[2*i]) - 127.5
		im := float64(buf[2*i+1]) - 127.5
		m := math.Sqrt(re*re + im*im)
		d.mag[d.head] = m
		d.head = (d.head + 1) % len(d.mag)
		if d.filled < len(d.mag) {
			d.filled++
		}
		// Slow noise-floor tracking (attenuated by clipping high peaks).
		att := m
		if att > d.noise*4 {
			att = d.noise * 4
		}
		d.noise += 0.0005 * (att - d.noise)
		d.count++
		if d.count%4 == 0 {
			d.tryPreamble()
		}
	}
}

// magAt returns the magnitude ~µs after a given absolute sample index.
func (d *ModeSDemod) at(abs int) float64 {
	idx := ((abs % len(d.mag)) + len(d.mag)) % len(d.mag)
	return d.mag[idx]
}

// abs converts ring position to an absolute sample index.
func (d *ModeSDemod) abs() int { return d.count }

// energy sums magnitude over ±halfWidth samples around a µs position
// relative to start (in samples).
func (d *ModeSDemod) energy(startSample int, us, halfWidth float64) float64 {
	c := startSample + int(us*d.rate/1e6)
	w := halfWidth * d.rate / 1e6
	var e float64
	for k := -int(w); k <= int(w); k++ {
		e += d.at(c + k)
	}
	return e
}

// magAt interpolates the magnitude at a fractional sample position —
// PPM chips straddle two samples at non-integer samples-per-bit, and
// window sums split their energy ~60/40, letting noise flip near-equal
// chips. The chip-centre interpolation keeps the decision on the
// stronger sample.
func (d *ModeSDemod) magAt(pos float64) float64 {
	i := int(math.Floor(pos))
	frac := pos - float64(i)
	a := d.at(i)
	b := d.at(i + 1)
	return a*(1-frac) + b*frac
}

// slotEnergy integrates magnitude over the half-bit slot starting at
// usOff microseconds after t0 — the PPM decision needs the FULL chip
// energy because at non-integer samples-per-bit any fixed sample point
// drifts out of the chip within a few bits.
func (d *ModeSDemod) slotEnergy(t0 int, usOff float64) float64 {
	start := float64(t0) + usOff*d.rate/1e6
	end := start + 0.5*d.rate/1e6
	var e float64
	for i := int(start + 0.5); i < int(end+0.5); i++ {
		e += d.at(i)
	}
	return e
}

// tryPreamble hunts for a Mode S preamble ending right at the newest
// samples and decodes the message that follows.
func (d *ModeSDemod) tryPreamble() {
	// Need 121 µs of history after the preamble start.
	need := int(125e-6 * d.rate)
	if d.filled < need {
		return
	}
	// Candidate preamble start: scan the recent 300 µs window
	// (messages overlap-check happens by consuming).
	to := d.abs() - need
	from := to - int(300e-6*d.rate)
	if from < d.abs()-len(d.mag) {
		from = d.abs() - len(d.mag)
	}
	thr := d.noise * 2.2
	if modesDebug != nil {
		modesDebug("scan from=%d to=%d abs=%d filled=%d", from, to, d.abs(), d.filled)
	}
	for t0 := from; t0 <= to; t0++ {
		// Preamble with full-window energies (shaped pulses leak onto
		// neighbouring samples; single-sample probes see shoulders).
		p1 := d.slotEnergy(t0, 0)
		if p1 < thr {
			continue
		}
		p2 := d.slotEnergy(t0, 1)
		p3 := d.slotEnergy(t0, 2)
		p4 := d.slotEnergy(t0, 3)
		g1 := d.slotEnergy(t0, 0.5)
		g2 := d.slotEnergy(t0, 1.5)
		g3 := d.slotEnergy(t0, 2.5)
		g4 := d.slotEnergy(t0, 4.5)
		peak := math.Min(math.Min(p1, p2), math.Min(p3, p4))
		gap := math.Max(math.Max(g1, g2), math.Max(g3, g4))
		if modesDebug != nil {
			modesDebug("cand t0=%d p=%.0f/%.0f/%.0f/%.0f g=%.0f/%.0f/%.0f/%.0f thr=%.0f", t0, p1, p2, p3, p4, g1, g2, g3, g4, thr)
		}
		if peak < thr*1.1 || peak < gap*1.35 {
			continue
		}
		d.Preambles++
		if modesDebug != nil {
			sps := d.rate / 1e6
			d2 := []byte{}
			for k := 0; k < 8; k++ {
				base := float64(t0) + (8.0+float64(k))*sps
				e1 := d.magAt(base + 0.25*sps)
				e2 := d.magAt(base + 0.75*sps)
				if e1 >= e2 {
					d2 = append(d2, '1')
				} else {
					d2 = append(d2, '0')
				}
			}
			modesDebug("PASS t0=%d firstbits=%s (want 10001101)", t0, string(d2))
		}
		// The preamble window quantises t0 to a whole sample; at
		// 2.4 MSPS that is a 0.42 µs error — most of a chip. Try small
		// offsets around it and let the CRC pick the truth.
		// Refine the time base: centre of the FIRST preamble pulse is
		// 0.25 µs after t0 — anchor on the strongest sample in the
		// first microsecond instead of the quantised window grid.
		sps := d.rate / 1e6
		bestV, bestI := -1.0, t0
		for i := t0; i < t0+int(1.2*sps); i++ {
			if v := d.at(i); v > bestV {
				bestV, bestI = v, i
			}
		}
		// Sample i spans [i, i+1), so its centre is i+0.5; the preamble
		// pulse-1 centre sits 0.25 µs after t0.
		centre := float64(bestI) + 0.5 - 0.25*sps
		if modesDebug != nil {
			modesDebug("t0=%d centre=%.2f", t0, centre)
		}
		for off := -1.5; off <= 1.51; off += 0.25 {
			ok, bits := d.decodeBits(centre + off)
			if modesDebug != nil {
				b8 := ""
				for _, b := range bits {
					b8 += string('0' + b)
				}
				modesDebug("off=%+.2f bits=%s crc0=%v", off, b8, ok)
			}
			if ok {
				break
			}
		}
		// Skip past this message before scanning again.
		t0 += int(120e-6 * d.rate)
	}
}

// decodeAt samples one message after a preamble at t0 and feeds the
// store on CRC success.
func (d *ModeSDemod) decodeAt(t0 int) bool {
	return d.decodeAtF(float64(t0))
}

func (d *ModeSDemod) decodeBits(t0 float64) (bool, []byte) {
	sps := d.rate / 1e6
	bits := make([]byte, 0, 112)
	for k := 0; k < 112; k++ {
		base := t0 + (8.0+float64(k))*sps
		// The chip decision asks the SAME predicate the modulator
		// answers: which samples have their CENTRE inside each half-bit
		// window. Max over those samples; empty windows fall back to
		// interpolation so fractional rates never produce an empty side.
		c0 := t0 + (8.0+float64(k))*sps
		c1 := c0 + 0.5*sps
		e1, e2 := 0.0, 0.0
		n1, n2 := 0, 0
		for i := int(c0); i <= int(c1)+1; i++ {
			c := float64(i) + 0.5
			if c >= c0 && c < c1 {
				if v := d.at(i); v > e1 {
					e1 = v
				}
				n1++
			}
		}
		c2 := c0 + sps
		for i := int(c1); i <= int(c2)+1; i++ {
			c := float64(i) + 0.5
			if c >= c1 && c < c2 {
				if v := d.at(i); v > e2 {
					e2 = v
				}
				n2++
			}
		}
		if n1 == 0 {
			e1 = d.magAt(c0 + 0.25*sps)
		}
		if n2 == 0 {
			e2 = d.magAt(c1 + 0.25*sps)
		}
		if e1 >= e2 {
			bits = append(bits, 1)
		} else {
			bits = append(bits, 0)
		}
		if modesDebug != nil && (k == 88 || k == 90) {
			var mm string
			for j := -2; j <= 4; j++ {
				mm += fmt.Sprintf(" %.0f", d.at(int(c0)+j))
			}
			modesDebug("k=%d c0=%.1f e1=%.0f e2=%.0f mags:%s", k, c0, e1, e2, mm)
		}
		if modesDebug != nil && false {
			modesDebug("k=%d e1=%.0f e2=%.0f base=%.2f m28..33=%.0f", k, e1, e2, base,
				d.magAt(float64(int(base))-4)+d.magAt(float64(int(base))-3)*0+d.at(int(base)-4)*0)
			for j := -3; j <= 3; j++ {
				modesDebug("   m[%d]=%.0f", int(base)+j, d.at(int(base)+j))
			}
		}
	}
	msg := bitsToBytes(bits)
	df := msg[0] >> 3
	nbits := 112
	if df == 0 || df == 4 || df == 5 || df == 11 {
		nbits = 56
	}
	d.CrcFails++
	if crc24(bits[:nbits]) != 0 {
		return false, bits
	}
	d.Decoded++
	if d.store != nil {
		d.store.Decode(msg[:nbits/8])
	}
	return true, bits
}

func (d *ModeSDemod) decodeAtF(t0 float64) bool {
	ok, _ := d.decodeBits(t0)
	return ok
}

// DecodeBitsDebug exposes bit sampling for tests.
func (d *ModeSDemod) DecodeBitsDebug(t0 float64) (bool, []byte) {
	return d.decodeBits(t0)
}
