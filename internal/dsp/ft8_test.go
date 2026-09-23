package dsp

import (
	"math"
	"math/rand"
	"testing"
)

// --- test-side encoder (77 bits → CRC → LDPC → 79 tones) --------------

func packCall28(call string) uint64 {
	switch call {
	case "DE":
		return 0
	case "QRZ":
		return 1
	case "CQ":
		return 2
	}
	iarea := -1
	for i := len(call); i >= 2; i-- {
		if call[i-1] >= '0' && call[i-1] <= '9' {
			iarea = i
			break
		}
	}
	if iarea < 2 || iarea > 3 {
		panic("test call not in standard form: " + call)
	}
	c := call
	if iarea == 2 {
		c = " " + call
	}
	for len(c) < 6 {
		c += " "
	}
	idx := func(alpha string, ch byte) int64 {
		for i := 0; i < len(alpha); i++ {
			if alpha[i] == ch {
				return int64(i)
			}
		}
		return -1
	}
	i1 := idx(ft8A1, c[0])
	i2 := idx(ft8A2, c[1])
	i3 := idx(ft8A3, c[2])
	i4 := idx(ft8A4, c[3])
	i5 := idx(ft8A4, c[4])
	i6 := idx(ft8A4, c[5])
	if i1 < 0 || i2 < 0 || i3 < 0 || i4 < 0 || i5 < 0 || i6 < 0 {
		panic("bad call " + call)
	}
	return uint64(ft8NTokens + ft8Max22 +
		i1*36*10*27*27*27 + i2*10*27*27*27 + i3*27*27*27 + i4*27*27 + i5*27 + i6)
}

func packGrid15(g string) uint64 {
	return uint64(int64(g[0]-'A')*1800 + int64(g[1]-'A')*100 + int64(g[2]-'0')*10 + int64(g[3]-'0'))
}

func pack77(call1, call2, grid string) []int {
	b := make([]int, 77)
	put := func(from int, v uint64, n int) {
		for i := 0; i < n; i++ {
			b[from+i] = int((v >> uint(n-1-i)) & 1)
		}
	}
	put(0, packCall28(call1), 28)
	put(28, 0, 1)
	put(29, packCall28(call2), 28)
	put(57, 0, 1)
	put(58, 0, 1)
	put(59, packGrid15(grid), 15)
	put(74, 1, 3)
	return b
}

func encodeTones(payload []int) []int {
	var pbytes [10]byte
	ft8PackBits(payload, 77, pbytes[:])
	var a91 [12]byte
	ft8AddCRC(pbytes[:], a91[:])
	var cw [22]byte
	ft8Encode174(a91[:], cw[:])
	bits := make([]int, 174)
	mask := byte(0x80)
	bi := 0
	for i := 0; i < 174; i++ {
		bits[i] = int(cw[bi] & mask & 1)
		if mask&1 == 1 && cw[bi]&mask != 0 {
			bits[i] = 1
		}
		if cw[bi]&mask != 0 {
			bits[i] = 1
		} else {
			bits[i] = 0
		}
		mask >>= 1
		if mask == 0 {
			mask = 0x80
			bi++
		}
	}
	tones := make([]int, 79)
	k := 0
	for i := 0; i < 79; i++ {
		if i < 7 {
			tones[i] = ft8SyncCostas[i]
		} else if i >= 36 && i < 43 {
			tones[i] = ft8SyncCostas[i-36]
		} else if i >= 72 {
			tones[i] = ft8SyncCostas[i-72]
		} else {
			tones[i] = FT8GrayMap[bits[3*k]<<2|bits[3*k+1]<<1|bits[3*k+2]]
			k++
		}
	}
	return tones
}

// synthFrame renders tones as 8 kHz audio with CONTINUOUS phase
// across symbols — per-symbol random phase splatters energy across
// the band (10-19 dB of artificial noise near the signal) that real
// FT8 does not have. Tone t of the group at groupHz sits at
// groupHz + (t-3.5)*6.25.
func synthFrame(tones []int, groupHz, amp, noise float64, rng *rand.Rand) []float64 {
	out := make([]float64, FT8FrameSamp)
	phase := rng.Float64() * 2 * math.Pi
	for sym, tone := range tones {
		f := groupHz + (float64(tone)-3.5)*FT8ToneHz
		w := 2 * math.Pi * f / float64(FT8AudioRate)
		for i := 0; i < FT8SymSamples; i++ {
			out[sym*FT8SymSamples+i] = amp * math.Sin(phase)
			phase += w
		}
	}
	if noise > 0 {
		for i := range out {
			out[i] += noise * rng.NormFloat64()
		}
	}
	return out
}

// feedRing pushes lead + signal (+ tail) through the detector.
func feedRing(d *FT8Detector, sig []float64, leadSamples int, noise float64, rng *rand.Rand) {
	chunk := 512
	push := func(v []float64) {
		for i := 0; i < len(v); i += chunk {
			e := i + chunk
			if e > len(v) {
				e = len(v)
			}
			d.Feed(v[i:e])
		}
	}
	lead := make([]float64, leadSamples)
	for i := range lead {
		lead[i] = noise * rng.NormFloat64()
	}
	push(lead)
	push(sig)
}

func drainText(d *FT8Detector) []string {
	d.Process()
	var out []string
	for _, m := range d.TakeMessages() {
		if m.Valid {
			out = append(out, m.Text)
		}
	}
	return out
}
var fftTestInputRe, fftTestInputIm []float64

func re0(n int) float64 { return fftTestInputRe[n] }
func im0(n int) float64 { return fftTestInputIm[n] }

func TestFFT2560Full(t *testing.T) {
	rng := rand.New(rand.NewSource(9))
	const N = ft8WFNFFT
	inRe := make([]float64, N)
	inIm := make([]float64, N)
	for i := 0; i < N; i++ {
		inRe[i] = rng.NormFloat64()
		inIm[i] = rng.NormFloat64()
	}
	fftTestInputRe = append([]float64(nil), inRe...)
	fftTestInputIm = append([]float64(nil), inIm...)
	fft2560(inRe, inIm)
	for _, k := range []int{0, 1, 7, 320, 640, 1234, 2000, 2559} {
		var sr, si float64
		for n := 0; n < N; n++ {
			a := -2 * math.Pi * float64(k) * float64(n) / float64(N)
			sr += re0(n)*math.Cos(a) - im0(n)*math.Sin(a)
			si += re0(n)*math.Sin(a) + im0(n)*math.Cos(a)
		}
		if math.Hypot(inRe[k]-sr, inIm[k]-si) > 1e-6*math.Max(1, math.Hypot(sr, si)) {
			t.Errorf("bin %d: got (%.6f,%.6f) want (%.6f,%.6f)", k, inRe[k], inIm[k], sr, si)
		}
	}
}

// TestWFDecodeClean: strong signal, exact grid → must decode.
func TestWFDecodeClean(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	tones := encodeTones(pack77("CQ", "HS0ZKO", "OK04"))
	sig := synthFrame(tones, 1200.0, 1.0, 0.1, rng)
	d := NewFT8Detector()
	d.SetEnabled(true)
	feedRing(d, sig, 2*FT8SymSamples, 0.1, rng)
	texts := drainText(d)
	found := false
	for _, s := range texts {
		if s == "CQ HS0ZKO OK04" {
			found = true
		}
	}
	if !found {
		t.Fatalf("clean decode failed, got %v", texts)
	}
}

// TestWFDecodeOffGrid: +2 Hz carrier offset, weak signal — the case
// that broke the old snapshot-FFT finder.
func TestWFDecodeOffGrid(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	tones := encodeTones(pack77("E20ZKT", "BA7SAY", "OL53"))
	sig := synthFrame(tones, 1500.0+2.0, 1.0, 0.55, rng)
	d := NewFT8Detector()
	d.SetEnabled(true)
	feedRing(d, sig, 2*FT8SymSamples, 0.55, rng)
	texts := drainText(d)
	found := false
	for _, s := range texts {
		if s == "E20ZKT BA7SAY OL53" {
			found = true
		}
	}
	if !found {
		t.Fatalf("off-grid weak decode failed, got %v", texts)
	}
}

// TestWFDecodeTwoSignals: overlapping transmissions decode in one scan.
func TestWFDecodeTwoSignals(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	t1 := synthFrame(encodeTones(pack77("CQ", "JA1ABC", "PM95")), 1200.0, 0.9, 0.35, rng)
	t2 := synthFrame(encodeTones(pack77("K1ABC", "W9XYZ", "EN37")), 2100.0, 0.9, 0.35, rng)
	mix := make([]float64, len(t1))
	for i := range mix {
		mix[i] = t1[i] + t2[i] + 0.35*rng.NormFloat64()
	}
	d := NewFT8Detector()
	d.SetEnabled(true)
	feedRing(d, mix, 2*FT8SymSamples, 0, rng)
	texts := drainText(d)
	set := map[string]bool{}
	for _, s := range texts {
		set[s] = true
	}
	if !set["CQ JA1ABC PM95"] || !set["K1ABC W9XYZ EN37"] {
		t.Fatalf("dual decode failed, got %v", texts)
	}
}

// TestWFDecodeVeryWeak: noise 1.0 (≈ -3..-6 dB in the tone bandwidth)
// — below what the old finder could even see; the waterfall
// correlation should still pull it out.
func TestWFDecodeVeryWeak(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	tones := encodeTones(pack77("K1ABC", "W9XYZ", "EN37"))
	sig := synthFrame(tones, 1800.0, 1.0, 1.0, rng)
	d := NewFT8Detector()
	d.SetEnabled(true)
	feedRing(d, sig, 2*FT8SymSamples, 1.0, rng)
	texts := drainText(d)
	found := false
	for _, s := range texts {
		if s == "K1ABC W9XYZ EN37" {
			found = true
		}
	}
	if !found {
		t.Fatalf("very weak decode failed (this is the regression target), got %v", texts)
	}
}

// TestWFSNRScale checks the SNR is in the FT8-standard 2500 Hz
// convention: a clean signal reads strongly positive, a deep-weak one
// negative, and they order correctly (this is the scale WSJT-X and
// pskreporter display).
func TestWFSNRScale(t *testing.T) {
	payload := pack77("K1ABC", "W9XYZ", "EN37")
	tones := encodeTones(payload)
	measure := func(noise float64, seed int64) float64 {
		rng := rand.New(rand.NewSource(seed))
		sig := synthFrame(tones, 1800.0, 1.0, noise, rng)
		wf := newFT8Waterfall()
		wf.feed(make([]float64, ft8WFNFFT))
		wf.feed(sig)
		cands := wf.findCandidates(5, 10)
		best := -999.0
		for _, c := range cands {
			if s := wf.candSNRDb(c); s > best {
				best = s
			}
		}
		return best
	}
	clean := measure(0.05, 41)
	mid := measure(0.5, 42)
	weak := measure(1.4, 43)
	t.Logf("SNR clean=%.1f dB, mid=%.1f dB, weak=%.1f dB (2500 Hz convention)", clean, mid, weak)
	if clean < 10 {
		t.Errorf("clean signal should read strongly positive, got %.1f", clean)
	}
	if weak > 0 {
		t.Errorf("very weak signal should read negative, got %.1f", weak)
	}
	if !(clean > mid && mid > weak) {
		t.Errorf("SNR not monotonic: %.1f > %.1f > %.1f", clean, mid, weak)
	}
}
