// Mode S demodulation from raw rtl_tcp IQ: envelope detection,
// preamble matching, PPM bit sampling and CRC-24 validation, feeding
// the same DF17/DF18 decoder the Beast path uses. Designed for
// capture rates ≥ ~2 MSPS (positions are computed in microseconds so
// no bit-drift accumulates at any rate).
package adsb

import (
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
	rate  float64
	store *Store

	// source-rate magnitude ring (linear-interpolation source)
	mag    []float64
	head   int
	filled int
	count  int     // source samples consumed
	srcPos float64 // source position of the next 2.4M bin

	// 2.4 MSPS magnitude ring (the decode domain)
	mag24    []float64
	head24   int
	filled24 int
	abs24    int // absolute 2.4M samples produced
	scanAt   int

	noise float64

	// Diagnostics.
	Preambles int
	CrcFails  int
	Decoded   int
}

func NewModeSDemod(rate float64, store *Store) *ModeSDemod {
	return &ModeSDemod{
		rate:  rate,
		store: store,
		mag:   make([]float64, 4096),
		mag24: make([]float64, 8192), // 2.4 MSPS magnitude ring (~3.4 ms)
		noise: 20,
	}
}

// at reads the source-rate ring at an absolute source sample index.
// Only the last couple of samples are ever read (the 2.4M grid lags
// the newest source sample by one), so the ring stays small.
func (d *ModeSDemod) at(abs int) float64 {
	idx := ((abs % len(d.mag)) + len(d.mag)) % len(d.mag)
	return d.mag[idx]
}

// readsb's hand-tuned per-phase correlators at 2.4 MSPS (one sample
// per 0.4167 us): each decides one PPM bit from 3-4 neighbouring
// magnitude samples. Ported from wiedehopf/readsb demod_2400.c
// (GPL-2.0-or-later; this file implements the algorithm, no code is
// copied verbatim beyond these coefficient tables).
func slicePhase(m []float64, i int, phase int) float64 {
	switch phase {
	case 0:
		return 18*m[i] - 15*m[i+1] - 3*m[i+2]
	case 1:
		return 14*m[i] - 5*m[i+1] - 9*m[i+2]
	case 2:
		return 16*m[i] + 5*m[i+1] - 20*m[i+2]
	case 3:
		return 7*m[i] + 11*m[i+1] - 18*m[i+2]
	default:
		return 4*m[i] + 15*m[i+1] - 20*m[i+2] + 1*m[i+3]
	}
}

// slice byte tables: for each running phase (0..4), eight
// {phaseFn, sampleOffset} pairs, MSB first; the phase advances and
// the pointer steps 19 samples per byte (20 on the wrap).
var sliceTables = [5][8][2]int{
	{{0, 0}, {2, 2}, {4, 4}, {1, 7}, {3, 9}, {0, 12}, {2, 14}, {4, 16}},
	{{1, 0}, {3, 2}, {0, 5}, {2, 7}, {4, 9}, {1, 12}, {3, 14}, {0, 17}},
	{{2, 0}, {4, 2}, {1, 5}, {3, 7}, {0, 10}, {2, 12}, {4, 14}, {1, 17}},
	{{3, 0}, {0, 3}, {2, 5}, {4, 7}, {1, 10}, {3, 12}, {0, 15}, {2, 17}},
	{{4, 0}, {1, 3}, {3, 5}, {0, 8}, {2, 10}, {4, 12}, {1, 15}, {3, 17}},
}

// FeedIQ consumes one block of interleaved u8 IQ.
//
// The magnitude stream is resampled onto the exact 2.4 MSPS grid the
// phase correlators were tuned for. Bin k samples the source signal at
// time k/2.4e6 via linear interpolation between the two source samples
// straddling it — exact when the source rate is a multiple of 2.4M and
// a correct band-limited resample for the higher rates.
//
// Rates BELOW 2.4 MSPS are not supported: Mode S is 1 Mbit/s PPM
// (≈2 MHz occupied), so at 2.048M the pulses are already undersampled
// and no resampling can recover them (verified: the best alignment
// still yields 3 wrong bits, which CRC-24 cannot pass). readsb's own
// demod_2400.c likewise requires 2.4 MSPS. The caller must set the RTL
// rate to 2.4M for Mode S.
func (d *ModeSDemod) FeedIQ(buf []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rate < 2.4e6 {
		return
	}
	n := len(buf) / 2
	sps := d.rate / 2.4e6 // source samples per 2.4M bin
	for i := 0; i < n; i++ {
		re := float64(buf[2*i]) - 127.5
		im := float64(buf[2*i+1]) - 127.5
		m := math.Sqrt(re*re + im*im)
		d.mag[d.head] = m
		d.head = (d.head + 1) % len(d.mag)
		if d.filled < len(d.mag) {
			d.filled++
		}
		att := m
		if att > d.noise*4 {
			att = d.noise * 4
		}
		d.noise += 0.0005 * (att - d.noise)
		d.count++
		// Emit every bin whose two interpolation source samples exist.
		for {
			fp := d.srcPos // source position of bin d.abs24, in samples
			i0 := int(fp)
			if float64(i0+1) > float64(d.count-1) {
				break
			}
			f := fp - float64(i0)
			v := d.at(i0)*(1-f) + d.at(i0+1)*f
			d.emit24(v)
			d.srcPos += sps
		}
	}
	d.scan24()
}

// at24 reads the 2.4M ring at an absolute 2.4M sample index.
func (d *ModeSDemod) at24(abs int) float64 {
	idx := ((abs % len(d.mag24)) + len(d.mag24)) % len(d.mag24)
	return d.mag24[idx]
}

// emit24 pushes one finished 2.4M magnitude into the ring.
func (d *ModeSDemod) emit24(v float64) {
	d.mag24[d.head24] = v
	d.head24 = (d.head24 + 1) % len(d.mag24)
	d.filled24++
	d.abs24++
}

// scan24 runs readsb's preamble hunt + phase-scored decode over the
// freshly appended 2.4M magnitudes.
func (d *ModeSDemod) scan24() {
	need := 320 // ~133 us of 2.4M samples for a long message + margin
	// Resync BEFORE scanning: after a big block (or a stall) the scan
	// cursor can sit outside the ring's window; clamping first keeps
	// every sample the ring still holds scannable.
	if d.scanAt < d.abs24-len(d.mag24)+40 {
		d.scanAt = d.abs24 - len(d.mag24) + 40
		if d.scanAt < 0 {
			d.scanAt = 0
		}
	}
	for d.scanAt+need <= d.abs24 {
		pa := d.scanAt
		if modesDebug != nil && pa >= 4798 && pa <= 4802 {
			modesDebug("scan pa=%d m1=%.0f m7=%.0f m12=%.0f m14=%.0f m15=%.0f", pa, d.at24(pa+1), d.at24(pa+7), d.at24(pa+12), d.at24(pa+14), d.at24(pa+15))
		}
		// readsb's cheap pre-check (indices in us at 2.4M)
		if !(d.at24(pa+1) > d.at24(pa+7) && d.at24(pa+12) > d.at24(pa+14) && d.at24(pa+12) > d.at24(pa+15)) {
			d.scanAt++
			continue
		}
		base := d.at24(pa+5) + d.at24(pa+8) + d.at24(pa+16) + d.at24(pa+17) + d.at24(pa+18)
		ref := base * 4 / 32 // preambleThreshold 4, /32
		diff23 := d.at24(pa+2) - d.at24(pa+3)
		sum14 := d.at24(pa+1) + d.at24(pa+4)
		diff1011 := d.at24(pa+10) - d.at24(pa+11)
		common := sum14 - diff23 + d.at24(pa+9) + d.at24(pa+12)
		paMag := common - diff1011
		phases := []int{}
		if paMag >= ref {
			phases = append(phases, 4, 5)
		}
		paMag2 := common + diff1011
		if paMag2 >= ref {
			phases = append(phases, 6, 7)
		}
		paMag3 := sum14 + 2*diff23 + diff1011 + d.at24(pa+12)
		if paMag3 >= ref {
			phases = append(phases, 8)
		}
		if len(phases) == 0 {
			d.scanAt++
			continue
		}
		d.Preambles++
		decoded := false
		for _, ph := range phases {
			if d.decodePhase(pa, ph) {
				decoded = true
				break
			}
		}
		if decoded {
			d.scanAt += 288 // skip the whole 120 us message
		} else {
			d.scanAt++
		}
	}
}

// decodePhase slices the message at one preamble phase and validates
// the CRC; on success the frame goes to the store.
func (d *ModeSDemod) decodePhase(pa int, tryPhase int) bool {
	off := pa + 19 + tryPhase/5
	phase := tryPhase % 5
	msg := make([]byte, 14)
	for i := 0; i < 14; i++ {
		tab := sliceTables[phase]
		var b byte
		for k := 0; k < 8; k++ {
			if slicePhase(d.ringSlice(off+tab[k][1]), 0, tab[k][0]) > 0 {
				b |= 1 << (7 - k)
			}
		}
		msg[i] = b
		// 8 bits = 19.2 samples at 2.4M: the pointer takes 19 and the
		// 0.2 residue advances the fifth-sample phase by one; on the
		// 4→0 wrap the residue carries into a 20th sample.
		phase = (phase + 1) % 5
		off += 19
		if phase == 0 {
			off++
		}
	}
	df := msg[0] >> 3
	// readsb accepts only legal DF values (short: 0/4/5/11; long:
	// 16/17/18/20/21). Without this gate a one-bit-shifted slice can
	// still hit a zero CRC by chance — the shifted frame has an illegal
	// DF, so the whitelist rejects it and the correct alignment wins.
	short := df == 0 || df == 4 || df == 5 || df == 11
	long := df == 16 || df == 17 || df == 18 || df == 20 || df == 21
	if !short && !long {
		d.CrcFails++
		return false
	}
	nbits := 112
	if short {
		nbits = 56
	}
	bits := bytesToBits(msg[:nbits/8])
	if crc24(bits[:nbits]) != 0 {
		d.CrcFails++
		return false
	}
	d.Decoded++
	if d.store != nil {
		d.store.Decode(msg[:nbits/8])
	}
	return true
}

// ringSlice returns the 2.4M ring positioned so index 0 == abs.
func (d *ModeSDemod) ringSlice(abs int) []float64 {
	out := make([]float64, 21)
	for i := range out {
		out[i] = d.at24(abs + i)
	}
	return out
}
