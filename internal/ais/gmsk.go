package ais

import "fmt"

// SetDbgTrace installs a test-aid trace hook (nil in production).
func SetDbgTrace(f func(string)) { dbgTrace = f }

var dbgTrace func(string)

// AIS over-the-air demodulator (ITU-R M.1371): 9600 bps GMSK, NRZI,
// HDLC framing, X.25 FCS. One ChannelDemod per RF channel
// (161.975 / 162.025). Feed() takes the FM-discriminator output plus
// the pre-detection power of one decimated channel; bursts are
// energy-detected, the bit phase is acquired from the 24-bit training
// preamble, and fixed-clock sampling suffices for the rest of the
// burst (a 14 ppm clock error drifts < 0.02 bits over the longest
// frame — and the ppm carrier error becomes a DC term that NRZI
// decoding is immune to by construction).

type ChannelDemod struct {
	rate  float64
	spb   float64 // samples per bit
	emit  func(payload []byte, ch int)
	Ch    int    // channel index passed to the callback (0 = A, 1 = B)
	CName string // "A" / "B"

	noise   float64 // slow-tracked noise floor (power domain)
	trend   float64 // fast power EMA — burst start/end decisions
	warmN   int     // floor warm-up sample counter
	warmSum float64
	inBurst bool
	below   int // consecutive samples below the end threshold
	buf     []float64
}

// NewChannelDemod builds a demod for an FM output sampled at rate Hz.
func NewChannelDemod(rate int, chIdx int, name string, emit func(payload []byte, ch int)) *ChannelDemod {
	return &ChannelDemod{
		rate:  float64(rate),
		spb:   float64(rate) / 9600.0,
		emit:  emit,
		Ch:    chIdx,
		CName: name,
		noise: -1,
	}
}

// Feed consumes one block of FM samples with matching pre-detection
// power samples (both at the demod's sample rate).
func (d *ChannelDemod) Feed(fm, power []float64) {
	for i := 0; i < len(fm); i++ {
		p := power[i]
		y := fm[i]
		if d.warmN < 64 {
			// Floor warm-up: seeding from a single (possibly
			// near-zero) noise sample pins the floor at ~0 — bursts
			// then start on nothing and never end.
			d.warmSum += p
			d.warmN++
			if d.warmN == 64 {
				d.noise = d.warmSum / 64
			}
			continue
		}
		// Sanitize phase-difference wraps (belt and braces — the DSP
		// front-end folds them, but a demod must survive any source).
		if y > 3.2 {
			y -= 6.283185307179586
		} else if y < -3.2 {
			y += 6.283185307179586
		}
		if d.noise < 0 {
			d.noise = p // first sample seeds the floor
		}
		// Fast trend (~16 samples) gates both burst edges: a single
		// noise spike must not start a burst, and once the channel
		// goes quiet the trend collapses quickly to end it (a frozen
		// threshold on exponential noise would never release).
		d.trend += (p - d.trend) / 16
		if !d.inBurst {
			// Slow symmetric EMA estimates the noise MEAN. An
			// asymmetric fast-down slew ratchets toward the minimum
			// of exponentially distributed noise power (samples sit
			// below the mean 63% of the time) and the floor collapses
			// to ~0 — bursts then fire on any flutter.
			d.noise += 0.01 * (p - d.noise)
			if d.trend > d.noise*2.5 {
				if dbgTrace != nil {
					dbgTrace(fmt.Sprintf("burst start: trend=%.4f noise=%.4f", d.trend, d.noise))
				}
				d.inBurst = true
				d.below = 0
				d.buf = d.buf[:0]
			}
			continue
		}
		d.buf = append(d.buf, y)
		if d.trend < d.noise*1.3 {
			d.below++
			if d.below > int(0.002*d.rate) { // 2 ms quiet = burst over
				d.inBurst = false
				if dbgTrace != nil {
					dbgTrace(fmt.Sprintf("burst end: n=%d", len(d.buf)))
				}
				d.decodeBurst()
			}
		} else {
			d.below = 0
		}
		if len(d.buf) > int(0.13*d.rate) { // max slot length guard
			d.inBurst = false
			d.decodeBurst()
		}
	}
}

// decodeBurst acquires the bit phase from the training preamble,
// samples symbols, NRZI-decodes and scans for HDLC frames.
func (d *ChannelDemod) decodeBurst() {
	n := len(d.buf)
	if n < int(d.spb*50) { // shorter than training+flag+min frame
		return
	}
	// The slicer needs a centred reference (LO error = DC here).
	var mean float64
	for _, y := range d.buf {
		mean += y
	}
	mean /= float64(n)
	// Integrate over the middle of each bit period instead of
	// point-sampling: ~3 dB of noise tolerance and it forgives a
	// quarter-bit of phase error.
	sample := func(pos float64) float64 {
		lo := int(pos + 0.25*d.spb)
		hi := int(pos + 0.75*d.spb)
		if lo < 0 {
			lo = 0
		}
		if hi > n {
			hi = n
		}
		if hi <= lo {
			if int(pos) >= 0 && int(pos) < n {
				return d.buf[int(pos)] - mean
			}
			return 0
		}
		sum := 0.0
		for k := lo; k < hi; k++ {
			sum += d.buf[k] - mean
		}
		return sum / float64(hi-lo)
	}
	lvl := func(x float64) int {
		if x >= 0 {
			return 1
		}
		return -1
	}

	// Skip the filter ramp-up, then try every quarter-sample phase.
	// The M.1371 training sequence is 0101… as DATA bits, so after the
	// NRZI decoder they alternate — score in the decoded-bit domain.
	const guardBits = 4
	bestScore, bestPhase := -1, 0.0
	for ph := 0.0; ph < d.spb; ph += 0.25 {
		// Sample guard+24 symbol levels, NRZI-decode to bits.
		var bits []byte
		prev := 0
		for i := 0; i < 25; i++ {
			pos := float64(guardBits+i-1)*d.spb + ph
			if int(pos) >= n {
				break
			}
			l := lvl(sample(pos))
			if i > 0 {
				if l == prev {
					bits = append(bits, 1)
				} else {
					bits = append(bits, 0)
				}
			}
			prev = l
		}
		if len(bits) < 24 {
			continue
		}
		scoreA, scoreB := 0, 0
		for i := 0; i < 24; i++ {
			if int(bits[i]) == i%2 {
				scoreA++
			} else {
				scoreB++
			}
		}
		score := scoreA
		if scoreB > scoreA {
			score = scoreB
		}
		if score > bestScore {
			bestScore, bestPhase = score, ph
		}
	}
	if dbgTrace != nil {
		dbgTrace(fmt.Sprintf("burst n=%d score=%d ph=%.2f", n, bestScore, bestPhase))
	}
	if bestScore < 16 { // alternating preamble not found
		return
	}

	// Sample the whole burst at the acquired phase (one leading symbol
	// seeds the NRZI decode).
	total := int(float64(n)/d.spb) - guardBits
	sym := make([]int, 0, total+1)
	for i := -1; i < total; i++ {
		pos := float64(guardBits+i)*d.spb + bestPhase
		if pos < 0 {
			sym = append(sym, 0)
			continue
		}
		if int(pos) >= n {
			break
		}
		sym = append(sym, lvl(sample(pos)))
	}
	// NRZI: transition = 0, no transition = 1.
	bits := make([]byte, 0, len(sym))
	for i := 1; i < len(sym); i++ {
		if sym[i] == sym[i-1] {
			bits = append(bits, 1)
		} else {
			bits = append(bits, 0)
		}
	}
	scanFrames(bits, func(payload []byte) {
		if d.emit != nil {
			d.emit(payload, d.Ch)
		}
	})
}

// scanFrames finds 0x7E flags, destuffs, checks the X.25 FCS and hands
// valid payloads to emit.
func scanFrames(bits []byte, emit func(payload []byte)) {
	flag := [8]byte{0, 1, 1, 1, 1, 1, 1, 0}
	var pos []int
	for i := 0; i+8 <= len(bits); i++ {
		match := true
		for k := 0; k < 8; k++ {
			if bits[i+k] != flag[k] {
				match = false
				break
			}
		}
		if match {
			pos = append(pos, i)
		}
	}
	if dbgTrace != nil {
		dbgTrace(fmt.Sprintf("scan: %d flags", len(pos)))
	}
	for f := 0; f+1 < len(pos); f++ {
		if pos[f+1] <= pos[f]+8 {
			continue // corrupt bit stream can yield bogus neighbours
		}
		raw := bits[pos[f]+8 : pos[f+1]]
		// Destuffed payload+FCS: 168..1172 payload bits + 16 FCS.
		if len(raw) < 168+16 || len(raw) > 1200+16+2*40 {
			continue
		}
		// Destuff (a short frame may legally contain no stuffing at
		// all — do not require any).
		out := make([]byte, 0, len(raw))
		ones := 0
		valid := true
		for _, b := range raw {
			if ones == 5 {
				if b == 0 {
					ones = 0
					continue // remove the stuffed 0
				}
				valid = false
				break // six 1s is only legal inside a flag
			}
			out = append(out, b)
			if b == 1 {
				ones++
			} else {
				ones = 0
			}
		}
		if dbgTrace != nil {
			dbgTrace(fmt.Sprintf("cand: raw=%d out=%d valid=%v", len(raw), len(out), valid))
		}
		if !valid || len(out)%8 != 0 || len(out) < 168+16 {
			continue
		}
		// HDLC transmits each octet LSB-first.
		frame := make([]byte, len(out)/8)
		for i := range frame {
			for k := 0; k < 8; k++ {
				frame[i] |= out[i*8+k] << k
			}
		}
		data := frame[:len(frame)-2]
		fcs := frame[len(frame)-2:]
		if crcX25(data) != uint16(fcs[0])|uint16(fcs[1])<<8 {
			if dbgTrace != nil {
				dbgTrace(fmt.Sprintf("crc fail: calc=%04X got=%02X%02X", crcX25(data), fcs[1], fcs[0]))
			}
			continue
		}
		cp := make([]byte, len(data))
		copy(cp, data)
		emit(cp)
	}
}

// crcX25 is the CRC-16/X.25 FCS of ITU-R M.1371 (reflected 0x1021,
// init 0xFFFF, xorout 0xFFFF). Check value: "123456789" → 0x906E.
func crcX25(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, ch := range b {
		crc ^= uint16(ch)
		for k := 0; k < 8; k++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFF
}
