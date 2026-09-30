package ais

import (
	"math"
	"sort"
	"time"
)

// AIS over-the-air demodulator, ported from AIS-catcher's proven
// "ModelDefault" architecture (jvde-github/AIS-catcher, GPL-3 —
// Source/DSP/Demod.{h,cpp}, Source/Marine/AIS.{h,cpp}):
//
//	Coherent matched filter (Filters::Coherent)
//	→ PhaseSearchEMA: 16-phase projection classifier, per-sample soft
//	  NRZI bit (XOR of adjacent decisions at the EMA-tracked best phase)
//	→ deinterleave into 5 parallel symbol-phase streams (48 ks/s =
//	  exactly 5 samples/symbol)
//	→ 5 HDLC decoder state machines (TRAINING→STARTFLAG→DATAFCS) with
//	  bit stuffing, X.25 FCS (residue 0xF0B8) and type/length
//	  plausibility early-abort (cannotBeValid)
//
// The earlier from-scratch demod (FM discriminator + burst energy
// detector + preamble phase acquisition) decoded live traffic but
// missed frames and needed careful threshold tuning — this port has no
// thresholds to tune and no timing loop at all: the 5 parallel
// decoders cover every sub-symbol phase, and CRC gates the rest.

// aisSampleRate is the fixed working rate: 48000/9600 = 5 samples per
// symbol exactly, which the parallel decoder bank relies on.
const aisSampleRate = 48000

// coherentTaps is AIS-catcher's Filters::Coherent — a 17-tap Gaussian
// pulse matched filter for the 9600 baud BT=0.3 GMSK at 48 ks/s.
var coherentTaps = []float64{
	2.06995719e-06, 3.18610148e-05, 3.40605309e-04, 2.52892989e-03,
	1.30411453e-02, 4.67076746e-02, 1.16186141e-01, 2.00730781e-01,
	2.40861391e-01, 2.00730781e-01, 1.16186141e-01, 4.67076746e-02,
	1.30411453e-02, 2.52892989e-03, 3.40605309e-04, 3.18610148e-05,
	2.06995719e-06,
}

// projPhase: the 8 classifier directions at (2j+1)·5.625° (Demod::phase,
// 16 phases across 180°); the mirrored phases come from the (a-b) branch.
var projPhase = [8][2]float64{
	{9.9518472640441780e-01, 9.8017143048367339e-02},
	{9.5694033335306883e-01, 2.9028468509743588e-01},
	{8.8192125790916542e-01, 4.7139674887287397e-01},
	{7.7301046123076901e-01, 6.3439329894649099e-01},
	{6.3439326515712957e-01, 7.7301046896098113e-01},
	{4.7139671032286945e-01, 8.8192127851457169e-01},
	{2.9028464326824340e-01, 9.5694034604181499e-01},
	{9.8017099547454692e-02, 9.9518473068888239e-01},
}

const nPhases = 16

// decoder states (AIS::Decoder::State).
const (
	stTraining = iota
	stStartFlag
	stDataFCS
)

const (
	minTrainingBits = 4
	maxFrameBits    = 1064 + 16 + 7
)

// frameDecoder is the per-symbol-phase HDLC machine (AIS::Decoder::Run).
type frameDecoder struct {
	prev, lastBit byte
	state         int
	position      int
	oneSeq        int
	bits          []byte
	emit          func(payload []byte, ch int, levelDb float64)
	ch            int

	// Signal level: sum of |z|² over the frame's symbol samples
	// (AIS-catcher's tag.level recipe) → mean power → dB.
	lvlSum float64
	lvlN   int
}

func (d *frameDecoder) reset() {
	d.state = stTraining
	d.position = 0
	d.oneSeq = 0
	d.bits = d.bits[:0]
}

// run consumes one transition indicator (1 = the carrier transitioned
// this sample) and runs the state machine (AIS::Decoder::Run).
func (d *frameDecoder) run(transition byte) {
	// NRZI: no transition = 1, transition = 0.
	nrzi := byte(1)
	if transition != d.prev {
		nrzi = 0
	}
	d.prev = transition

	switch d.state {
	case stTraining:
		if nrzi != d.lastBit {
			d.position++
		} else {
			if d.position > minTrainingBits {
				d.state = stStartFlag
				d.position = 1
				if nrzi == 1 {
					d.position = 3
				}
				d.oneSeq = 0
			} else {
				d.position = 0
			}
		}
	case stStartFlag:
		if d.position == 7 {
			if nrzi == 0 {
				d.state = stDataFCS
				d.position = 0
				d.oneSeq = 0
				d.bits = d.bits[:0]
				d.lvlSum, d.lvlN = 0, 0
			} else {
				d.reset()
			}
		} else {
			if nrzi == 1 {
				d.position++
			} else {
				d.reset()
			}
		}
	case stDataFCS:
		d.bits = append(d.bits, nrzi)
		d.lvlN++
		if nrzi == 1 {
			if d.oneSeq == 5 {
				// Six consecutive 1s: the closing flag starts here.
				// The last 7 appended bits (flag lead-in 0 + six 1s)
				// are not part of the frame.
				d.process(len(d.bits) - 7)
				d.reset()
				return
			}
			d.oneSeq++
		} else {
			if d.oneSeq == 5 {
				d.bits = d.bits[:len(d.bits)-1] // destuff
			}
			d.oneSeq = 0
		}
		if len(d.bits) >= maxFrameBits || d.cannotBeValid() {
			d.reset()
		}
	}
	d.lastBit = nrzi
}

// cannotBeValid early-aborts type/length mismatches (AIS::Decoder).
func (d *frameDecoder) cannotBeValid() bool {
	const end = 24
	n := len(d.bits)
	if n < 6+end {
		return false
	}
	t := d.field(0, 6)
	switch n {
	case 6 + end:
		return t > 28 || t == 0
	case 8 + 30 + end:
		return d.field(8, 30) > 999999999
	case 72 + end:
		return t == 10
	case 144 + end:
		return t == 16
	case 160 + end:
		return t == 15 || t == 20 || t == 23
	case 168 + end:
		return t == 1 || t == 2 || t == 3 || t == 4 || t == 7 || t == 9 || t == 11 || t == 18 || t == 22 || t == 24 || t == 25 || t == 27 || t == 28
	case 312 + end:
		return t == 19
	case 361 + end:
		return t == 21
	case 424 + end:
		return t == 5
	}
	return false
}

// field reads an unsigned bit field from the accumulated bits.
func (d *frameDecoder) field(off, n int) uint32 {
	var v uint32
	for i := 0; i < n && off+i < len(d.bits); i++ {
		v = v<<1 | uint32(d.bits[off+i])
	}
	return v
}

// DbgAttempts captures the longest bit sequences that reached the
// FCS stage even when the CRC failed (test aid for ground-truth
// comparison against a reference decoder).
var DbgAttempts [][]byte

// process CRC-checks data+FCS and emits the payload (AIS::processData;
// the running X.25 CRC over data+FCS must equal the 0xF0B8 residue).
func (d *frameDecoder) process(n int) {
	if n < 16 || n > len(d.bits) {
		return
	}
	crc := uint32(0xFFFF)
	for i := 0; i < n; i++ {
		if (uint32(d.bits[i])^crc)&1 != 0 {
			crc = (crc >> 1) ^ 0x8408
		} else {
			crc >>= 1
		}
	}
	if crc != 0xF0B8 {
		if len(DbgAttempts) < 400 {
			cp := append([]byte{}, d.bits[:n]...)
			DbgAttempts = append(DbgAttempts, cp)
		}
		return
	}
	nBits := n - 16
	if nBits%8 != 0 || nBits/8 < 21 {
		return
	}
	// AIS octets go on the air LSB-first (HDLC order) — verified
	// against AIS-catcher on a live recording: the X.25 FCS is fully
	// reflected, so a bit-swapped reading ALSO passes CRC, which is
	// exactly how the junk 94x MMSIs crept through the MSB build.
	payload := make([]byte, nBits/8)
	for i := range payload {
		for k := 0; k < 8; k++ {
			payload[i] |= d.bits[i*8+k] << k
		}
	}
	if d.emit != nil {
		lvl := -99.0
		if d.lvlN > 0 && d.lvlSum > 0 {
			lvl = 10 * math.Log10(d.lvlSum/float64(d.lvlN))
		}
		d.emit(payload, d.ch, lvl)
	}
}

// dupOfRecent reports whether this exact payload was emitted within
// the last second (several phase streams can decode the same burst —
// AIS-catcher cross-resets its decoders for the same effect).
func (d *ChannelDemod) dupOfRecent(payload []byte) bool {
	now := time.Now()
	if now.Sub(d.lastSeen) < time.Second && string(d.lastPay) == string(payload) {
		return true
	}
	d.lastPay = append(d.lastPay[:0], payload...)
	d.lastSeen = now
	return false
}

// phaseClassifier is the 16-phase projection demod (Demod::
// PhaseSearchEMA) running at SYMBOL rate — one complex sample per bit,
// as the ScatterPLL deinterleaver delivers. The (1j)**i un-rotation
// matches MSK's ±90°-per-symbol rotation, so the classifier only has
// to find (and slowly track) the carrier phase.
type phaseClassifier struct {
	rot   int
	ma    [nPhases]float64
	bitsH [nPhases]uint8
	maxI  int

	// No local frequency loop: AIS-catcher's SquareFreqOffsetCorrection
	// upstream (ffcRun) already removes the carrier offset at ~47 Hz
	// resolution each block; the EMA phase tracker below absorbs the
	// remainder.
}

// symbol consumes one symbol-spaced complex sample and returns the
// soft NRZI transition bit (XOR of the two most recent decisions at
// the EMA-tracked best phase).
func (p *phaseClassifier) symbol(re, im float64) byte {
	// Multiply by (1j)**i to pull the rotating constellation onto
	// a line.
	var cre, cim float64
	switch p.rot {
	case 0:
		cre, cim = re, im
	case 1:
		cre, cim = -im, re
	case 2:
		cre, cim = -re, -im
	case 3:
		cre, cim = im, -re
	}
	p.rot = (p.rot + 1) & 3

	// 16-phase linear classification: decision history + magnitude
	// EMA per phase; phases 8..15 via the (a-b) mirror.
	for j := 0; j < 8; j++ {
		a := cre * projPhase[j][0]
		b := cim * projPhase[j][1]
		t := a + b
		p.bitsH[j] = p.bitsH[j]<<1 | boolBit(t > 0)
		p.ma[j] = 0.85*p.ma[j] + 0.15*absF(t)
		t = a - b
		p.bitsH[nPhases-1-j] = p.bitsH[nPhases-1-j]<<1 | boolBit(t > 0)
		p.ma[nPhases-1-j] = 0.85*p.ma[nPhases-1-j] + 0.15*absF(t)
	}

	// Strongest phase. A full-circle argmax (rather than AIS-catcher's
	// ±2-bin window) tracks carrier offsets up to ~2.4 kHz — the real
	// dongle's residual ppm after the app's Freq Correction setting.
	// During idle noise the phases equalise and the pick wanders,
	// which is harmless: the CRC gates every frame.
	maxVal := p.ma[0]
	p.maxI = 0
	for k := 1; k < nPhases; k++ {
		if p.ma[k] > maxVal {
			maxVal = p.ma[k]
			p.maxI = k
		}
	}

	// Soft NRZI bit: XOR of the two most recent decisions.
	const nDelay = 1
	b2 := (p.bitsH[p.maxI] >> (nDelay + 1)) & 1
	b1 := (p.bitsH[p.maxI] >> nDelay) & 1
	if b1^b2 != 0 {
		return 1
	}
	return 0
}

// ChannelDemod decodes one AIS channel from complex IQ at 48 ks/s
// (exactly 5 samples per symbol).
type ChannelDemod struct {
	Ch    int
	CName string
	emit  func(payload []byte, ch int, levelDb float64)

	// Square-law frequency-offset correction (AIS-catcher's
	// SquareFreqOffsetCorrection): every 512 samples, FFT of z² locates
	// the GMSK spectrum's two halves (separated by exactly the symbol
	// rate), giving the residual carrier offset at ~47 Hz resolution —
	// the mechanism that makes real dongle ppm survivable. The block is
	// then derotated by the measured offset.
	ffcBuf []complex128
	ffcRot complex128
	ffcRe  []float64
	ffcIm  []float64
	ffcCum []float64

	firHist []complex128 // coherent filter history
	clas    [5]phaseClassifier
	dec     [5]frameDecoder
	sel     int

	// Duplicate suppression: several of the 5 phase streams can decode
	// the same burst (AIS-catcher cross-resets its decoders for this);
	// identical payloads within a second are dropped.
	lastPay  []byte
	lastSeen time.Time
}

// NewChannelDemod builds a demod for one channel; rate must be 48000.
func NewChannelDemod(rate int, chIdx int, name string, emit func(payload []byte, ch int, levelDb float64)) *ChannelDemod {
	if rate != aisSampleRate {
		panic("ais: ChannelDemod requires 48 ks/s")
	}
	d := &ChannelDemod{Ch: chIdx, CName: name, ffcRot: 1}
	filtered := func(p []byte, ch int, levelDb float64) {
		if !d.dupOfRecent(p) {
			emit(p, ch, levelDb)
		}
	}
	d.emit = filtered
	for i := range d.dec {
		d.dec[i].emit = filtered
		d.dec[i].ch = chIdx
	}
	return d
}

// Feed consumes one block of channel-centred complex samples at 48
// ks/s. Every 5th sample (round-robin) forms one symbol-spaced stream
// per sub-symbol phase — each stream feeds its own projection
// classifier and HDLC decoder. The aligned stream decodes; the others
// produce CRC failures. This is AIS-catcher's ScatterPLL arrangement
// and it replaces any timing recovery loop.
func (d *ChannelDemod) Feed(cx []complex128) {
	for _, z := range cx {
		d.ffcBuf = append(d.ffcBuf, z)
		if len(d.ffcBuf) == 512 {
			d.ffcRun()
			for _, c := range d.ffcBuf {
				d.sample(c)
			}
			d.ffcBuf = d.ffcBuf[:0]
		}
	}
}

// ffcRun measures and removes the block's frequency offset.
func (d *ChannelDemod) ffcRun() {
	const n = 512
	if d.ffcRe == nil {
		d.ffcRe = make([]float64, n)
		d.ffcIm = make([]float64, n)
	}
	for i, z := range d.ffcBuf {
		d.ffcRe[i] = real(z)*real(z) - imag(z)*imag(z)
		d.ffcIm[i] = 2 * real(z) * imag(z)
	}
	FFT(d.ffcRe, d.ffcIm)
	delta := int(math.Round(9600.0 / 48000.0 * float64(n))) // symbol-rate separation (bins)
	// Wide mode (their default): first locate the most energetic
	// spectral window, with a bonus for the bin pair one symbol-rate
	// apart — that is where the two GMSK spectrum halves sit.
	m := int(math.Round(12500.0 / 48000.0 * float64(n)))
	ofs := (m - delta) / 2
	if d.ffcCum == nil {
		d.ffcCum = make([]float64, n)
	}
	absAt := func(i int) float64 {
		return mag(d.ffcRe[(i+n/2)%n], d.ffcIm[(i+n/2)%n])
	}
	d.ffcCum[0] = 0
	for i := 1; i < n; i++ {
		d.ffcCum[i] = d.ffcCum[i-1] + absAt(i)
	}
	wm, wi := -1.0, 0
	for i := 0; i < n-m; i++ {
		v := d.ffcCum[i+m] - d.ffcCum[i] +
			0.6*(absAt(i+ofs)+absAt(i+ofs+delta))
		if v > wm {
			wm, wi = v, i
		}
	}
	wi = wi + m/2 - n/2

	// Fine peak: strongest bin pair straddling the measured centre.
	maxVal, bi := 0.0, 0
	const window = 187
	for i := wi + window; i < wi+n-window-delta; i++ {
		ii := ((i % n) + n) % n
		jj := ((i+delta)%n + n) % n
		h := mag(d.ffcRe[(ii+n/2)%n], d.ffcIm[(ii+n/2)%n]) +
			mag(d.ffcRe[(jj+n/2)%n], d.ffcIm[(jj+n/2)%n])
		if h > maxVal {
			maxVal, bi = h, ii
		}
	}
	// Idle gate: with no signal the peak is noise — derotating by it
	// only injects phase jumps the classifier must then survive.
	spec := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		spec = append(spec, mag(d.ffcRe[i], d.ffcIm[i]))
	}
	sort.Float64s(spec)
	if maxVal < 8*spec[n/2] {
		return
	}
	if false {
		return
	}
	fz := float64(n)/2 - (float64(bi) + float64(delta)/2)
	f := fz / 2 / float64(n)
	step := complex(math.Cos(f*2*math.Pi), math.Sin(f*2*math.Pi))
	for i := range d.ffcBuf {
		d.ffcRot *= step
		d.ffcBuf[i] *= d.ffcRot
	}
	d.ffcRot /= complex(math.Hypot(real(d.ffcRot), imag(d.ffcRot)), 0)
}

func mag(re, im float64) float64 { return math.Sqrt(re*re + im*im) }

// sample runs one derotated sample through the matched filter and the
// classifier bank.
func (d *ChannelDemod) sample(z complex128) {
	// Coherent matched filter (history buffer, newest last).
	d.firHist = append(d.firHist, z)
	if len(d.firHist) > len(coherentTaps) {
		copy(d.firHist, d.firHist[1:])
		d.firHist = d.firHist[:len(coherentTaps)]
	}
	var re, im float64
	for k, h := range d.firHist {
		tap := coherentTaps[len(coherentTaps)-1-k]
		re += real(h) * tap
		im += imag(h) * tap
	}

	soft := d.clas[d.sel].symbol(re, im)
	if d.dec[d.sel].state == stDataFCS {
		d.dec[d.sel].lvlSum += re*re + im*im
	}
	d.dec[d.sel].run(soft)
	d.sel = (d.sel + 1) % 5
}

func boolBit(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// crcX25 is the X.25 FCS (used by the test synthesizer).
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

// FFT is a compact in-place radix-2 FFT (power-of-two sizes) — the
// package cannot import internal/dsp (import cycle).
func FFT(re, im []float64) {
	n := len(re)
	if n&(n-1) != 0 {
		panic("ais: FFT size must be a power of two")
	}
	// Bit-reversal permutation.
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := -2 * math.Pi / float64(length)
		wRe, wIm := math.Cos(ang), math.Sin(ang)
		for i := 0; i < n; i += length {
			curRe, curIm := 1.0, 0.0
			for k := 0; k < length/2; k++ {
				a := i + k
				b := i + k + length/2
				tRe := re[b]*curRe - im[b]*curIm
				tIm := re[b]*curIm + im[b]*curRe
				re[b] = re[a] - tRe
				im[b] = im[a] - tIm
				re[a] += tRe
				im[a] += tIm
				nRe := curRe*wRe - curIm*wIm
				curIm = curRe*wIm + curIm*wRe
				curRe = nRe
			}
		}
	}
}
