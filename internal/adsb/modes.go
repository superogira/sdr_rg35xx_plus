// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Mode S demodulation from raw rtl_tcp IQ, ported from wiedehopf/readsb
// demod_2400.c (GPL-2.0-or-later): magnitude envelope resampled to exactly
// 2.4 MSPS, preamble pre-check, per-phase slice correlators, CRC-24
// validation, feeding the same DF17/DF18 decoder the Beast path uses.
//
// The inner loop mirrors readsb's integer arithmetic (uint16 magnitudes,
// int32 correlator products) on a CONTIGUOUS buffer — the float64/ring
// version this replaced measured slower than real time even on a desktop
// core, which the handheld's A53 could never sustain.
package adsb

import (
	"math"
	"sync"
)

// crc24tab is the byte-wise Mode S CRC table (poly 0xFFF409), built once.
var crc24tab [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		c := uint32(i) << 16
		for k := 0; k < 8; k++ {
			if c&0x800000 != 0 {
				c = (c << 1) ^ 0xFFF409
			} else {
				c <<= 1
			}
		}
		crc24tab[i] = c & 0xFFFFFF
	}
}

// crc24 is the Mode S checksum over packed bytes (MSB-first), computed
// byte-wise via the table — the bit-by-bit form was ~18% of demod CPU.
func crc24(msg []byte) uint32 {
	crc := uint32(0)
	for _, b := range msg {
		crc = (crc << 8) ^ crc24tab[(byte(crc>>16)^b)&0xFF]
		crc &= 0xFFFFFF
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

// AppendCRC24 computes the checksum of the data bits and appends its
// 24 bits (message assembler for tests / transmissions).
func AppendCRC24(data []byte) []byte {
	c := crc24(data)
	extra := make([]byte, 3)
	extra[0] = byte(c >> 16)
	extra[1] = byte(c >> 8)
	extra[2] = byte(c)
	return append(append([]byte{}, data...), extra...)
}

// modesDebug hooks demod tracing in tests.
var modesDebug func(f string, a ...any)

// magLUT maps a u8 IQ pair (re<<8|im) to its uint16 magnitude, scaled so
// full-scale (127.5,127.5) reaches 65535 — readsb's magnitude domain.
// Built once; a 65536-entry table beats a sqrt per sample.
var magLUT [65536]uint16

func init() {
	const scale = 65535.0 / (127.5 * math.Sqrt2)
	for re := 0; re < 256; re++ {
		r := float64(re) - 127.5
		for im := 0; im < 256; im++ {
			i := float64(im) - 127.5
			v := math.Sqrt(r*r+i*i) * scale
			if v > 65535 {
				v = 65535
			}
			magLUT[re<<8|im] = uint16(v)
		}
	}
}

// ModeSDemod turns raw u8 IQ at a known rate into Mode S messages.
type ModeSDemod struct {
	mu    sync.Mutex
	rate  float64
	store *Store

	// source-rate magnitude ring (resample source); power-of-two length.
	src    []uint16
	count  int     // source samples consumed
	srcPos float64 // source position of the next 2.4M bin

	// 2.4 MSPS magnitude buffer (readsb's uint16 domain), contiguous;
	// the scanned prefix is compacted away after each pass.
	mag24  []uint16
	abs24  int // absolute 2.4M index of mag24[0]
	scanAt int // absolute 2.4M index of the next scan position

	// Diagnostics.
	Preambles int
	CrcFails  int
	Decoded   int
}

func NewModeSDemod(rate float64, store *Store) *ModeSDemod {
	return &ModeSDemod{
		rate:  rate,
		store: store,
		src:   make([]uint16, 4096),
	}
}

// Rate returns the configured capture rate this demod was built for.
func (d *ModeSDemod) Rate() float64 { return d.rate }

// srcAt reads the source-rate ring at an absolute source sample index.
func (d *ModeSDemod) srcAt(abs int) uint16 {
	return d.src[abs&(len(d.src)-1)]
}

// slice byte tables: for each running phase (0..4), eight
// {phaseFn, sampleOffset} pairs, MSB first; the phase advances and the
// pointer steps 19 samples per byte (20 on the wrap). Ported from readsb.
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
// straddling it — exact when the source rate is a multiple of 2.4M.
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
		m := magLUT[uint16(buf[2*i])<<8|uint16(buf[2*i+1])]
		d.src[d.count&(len(d.src)-1)] = m
		d.count++
		// Emit every bin whose two interpolation source samples exist.
		for {
			fp := d.srcPos
			i0 := int(fp)
			if i0+1 > d.count-1 {
				break
			}
			f := fp - float64(i0)
			m0 := int32(d.srcAt(i0))
			m1 := int32(d.srcAt(i0 + 1))
			d.mag24 = append(d.mag24, uint16(m0+int32(float64(m1-m0)*f)))
			d.srcPos += sps
		}
	}
	d.scan24()
}

// scan24 runs readsb's preamble hunt + phase-scored decode over the
// contiguous 2.4M magnitude buffer.
func (d *ModeSDemod) scan24() {
	m := d.mag24
	rel := d.scanAt - d.abs24 // scan cursor relative to mag24[0]
	if rel < 0 {
		rel = 0
	}
	stop := len(m) - 320 // a long message + margin must fit
	for rel < stop {
		pa := m[rel:]
		// readsb's cheap pre-check (indices in us at 2.4M)
		if !(pa[1] > pa[7] && pa[12] > pa[14] && pa[12] > pa[15]) {
			rel++
			continue
		}
		baseNoise := int32(pa[5]) + int32(pa[8]) + int32(pa[16]) + int32(pa[17]) + int32(pa[18])
		ref := baseNoise * 4 / 32 // preambleThreshold 4, /32
		diff23 := int32(pa[2]) - int32(pa[3])
		sum14 := int32(pa[1]) + int32(pa[4])
		diff1011 := int32(pa[10]) - int32(pa[11])
		common := sum14 - diff23 + int32(pa[9]) + int32(pa[12])
		phases := [5]int{}
		nph := 0
		if common-diff1011 >= ref {
			phases[nph], phases[nph+1], nph = 4, 5, nph+2
		}
		if common+diff1011 >= ref {
			phases[nph], phases[nph+1], nph = 6, 7, nph+2
		}
		if sum14+2*diff23+diff1011+int32(pa[12]) >= ref {
			phases[nph], nph = 8, nph+1
		}
		if nph == 0 {
			rel++
			continue
		}
		d.Preambles++
		decoded := false
		for k := 0; k < nph; k++ {
			if d.decodePhase(pa, phases[k]) {
				decoded = true
				break
			}
		}
		if decoded {
			rel += 288 // skip the whole 120 us message
		} else {
			rel++
		}
	}
	d.scanAt = d.abs24 + rel
	// Compact: drop the scanned prefix so the buffer stays small. Keep
	// a little history before the cursor (the pre-check reads pa[1..18]
	// and a message may start a few samples before rel after a skip).
	if rel > 4096 {
		keep := rel - 64
		n := copy(m, m[keep:])
		d.mag24 = m[:n]
		d.abs24 += keep
		d.scanAt = d.abs24 + 64
	}
}

// decodePhase slices the message at one preamble phase and validates
// the CRC; on success the frame goes to the store.
func (d *ModeSDemod) decodePhase(pa []uint16, tryPhase int) bool {
	off := 19 + tryPhase/5
	phase := tryPhase % 5
	var msg [14]byte
	// Slice byte 0 first and bail on an illegal DF: on a noisy band
	// almost every preamble candidate is noise, and slicing all bytes
	// before the DF check would spend ~14x the work per candidate
	// (readsb's score_phase does the same early exit).
	msg[0] = sliceByteI(pa, &off, &phase)
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
	nbytes := 14
	if short {
		nbytes = 7
	}
	for i := 1; i < nbytes; i++ {
		msg[i] = sliceByteI(pa, &off, &phase)
	}
	if crc24(msg[:nbytes]) != 0 {
		d.CrcFails++
		return false
	}
	d.Decoded++
	if d.store != nil {
		d.store.Decode(msg[:nbytes])
	}
	return true
}

// sliceByteI slices one byte at the running phase and advances the
// pointer/phase exactly as readsb's slice_byte does.
func sliceByteI(pa []uint16, off *int, phase *int) byte {
	tab := sliceTables[*phase]
	var b byte
	for k := 0; k < 8; k++ {
		if slicePhaseI(pa, *off+tab[k][1], tab[k][0]) > 0 {
			b |= 1 << (7 - k)
		}
	}
	// 8 bits = 19.2 samples at 2.4M: the pointer takes 19 and the
	// 0.2 residue advances the fifth-sample phase by one; on the
	// 4→0 wrap the residue carries into a 20th sample.
	*phase = (*phase + 1) % 5
	*off += 19
	if *phase == 0 {
		*off++
	}
	return b
}

// slicePhaseI is readsb's hand-tuned per-phase correlator at 2.4 MSPS
// (one sample per 0.4167 us): each decides one PPM bit from 3-4
// neighbouring magnitude samples, in int32 arithmetic.
func slicePhaseI(m []uint16, i int, phase int) int32 {
	m0, m1, m2 := int32(m[i]), int32(m[i+1]), int32(m[i+2])
	switch phase {
	case 0:
		return 18*m0 - 15*m1 - 3*m2
	case 1:
		return 14*m0 - 5*m1 - 9*m2
	case 2:
		return 16*m0 + 5*m1 - 20*m2
	case 3:
		return 7*m0 + 11*m1 - 18*m2
	default:
		return 4*m0 + 15*m1 - 20*m2 + int32(m[i+3])
	}
}
