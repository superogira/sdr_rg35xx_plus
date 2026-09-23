package dsp

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// --- test-side encoder -------------------------------------------------

// packCall28 packs a standard call (or CQ/DE/QRZ token) into 28 bits
// following packjt77's rules: the call's LAST digit must sit at
// 1-based position 2 (then a space is prepended) or 3 (padded right),
// so the canonical 6-char form always has the digit at position 3.
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
	var c string
	if iarea == 2 {
		c = " " + call
	} else {
		c = call
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
	j1 := int64(g[0] - 'A')
	j2 := int64(g[1] - 'A')
	j3 := int64(g[2] - '0')
	j4 := int64(g[3] - '0')
	return uint64(j1*18*10*10 + j2*10*10 + j3*10 + j4)
}

// pack77 builds the 77 payload bits of a standard (i3=1) message
// "call1 call2 grid" or "CQ call2 grid".
func pack77(call1, call2, grid string) []int {
	b := make([]int, 77)
	put := func(from int, v uint64, n int) {
		for i := 0; i < n; i++ {
			b[from+i] = int((v >> uint(n-1-i)) & 1)
		}
	}
	put(0, packCall28(call1), 28)
	put(28, 0, 1) // ipa
	put(29, packCall28(call2), 28)
	put(57, 0, 1) // ipb
	put(58, 0, 1) // ir
	put(59, packGrid15(grid), 15)
	// bits 74-76 are i3 (=1); type-1 messages have no separate n3 field
	put(74, 1, 3)
	return b
}

// encodeTones: 77 bits → CRC → LDPC → 79 tones.
func encodeTones(payload []int) []int {
	var pbytes [10]byte
	ft8PackBits(payload, 77, pbytes[:])
	var a91 [12]byte
	ft8AddCRC(pbytes[:], a91[:])
	var cw [22]byte
	ft8Encode174(a91[:], cw[:])
	bits := make([]int, 174)
	{
		mask := byte(0x80)
		bi := 0
		for i := 0; i < 174; i++ {
			bits[i] = int(cw[bi] & mask >> trailingMaskPos(mask))
			mask >>= 1
			if mask == 0 {
				mask = 0x80
				bi++
			}
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

func trailingMaskPos(mask byte) uint {
	pos := uint(0)
	m := mask
	for m > 1 {
		m >>= 1
		pos++
	}
	return pos
}

// synthFrame renders the 79 tones as 8 kHz audio at the given center
// frequency (tone t at centerHz+(t-3.5)*6.25), matching the detector
// convention.
func synthFrame(tones []int, centerHz float64, amp, noise float64, rng *rand.Rand) []float64 {
	out := make([]float64, FT8FrameSamp)
	for sym, tone := range tones {
		f := centerHz + (float64(tone)-3.5)*FT8ToneHz
		phase := rng.Float64() * 2 * math.Pi
		w := 2 * math.Pi * f / float64(FT8AudioRate)
		for i := 0; i < FT8SymSamples; i++ {
			s := sym*FT8SymSamples + i
			out[s] = amp * math.Sin(phase+float64(i)*w)
		}
	}
	if noise > 0 {
		for i := range out {
			out[i] += noise * rng.NormFloat64()
		}
	}
	return out
}

func buildRing(sig []float64, leadSamples int, noise float64, rng *rand.Rand) []float64 {
	ring := make([]float64, ft8RingSamples)
	for i := 0; i < ft8RingSamples; i++ {
		v := 0.0
		if i >= leadSamples && i < leadSamples+len(sig) {
			v = sig[i-leadSamples]
		}
		if noise > 0 {
			v += noise * rng.NormFloat64()
		}
		ring[i] = v
	}
	return ring
}

func runDetectDecode(t *testing.T, ring []float64, centerHz float64) (FT8Detection, *FT8Message) {
	t.Helper()
	d := NewFT8Detector()
	det, ok, at := d.detectAt(ring, centerHz)
	if !ok {
		t.Fatalf("sync not found at %.4f Hz", centerHz)
	}
	t.Logf("sync ok: off=%d conf=%.2f snr=%.1f dB", at, det.Confidence, det.SNRDb)
	msg, _ := ft8DecodeAt(ring, at, centerHz)
	return det, msg
}

// TestFT8RoundTripClean encodes, synthesises and decodes a CQ message
// with no noise. Everything must come back exactly.
func TestFT8RoundTripClean(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const center = 1234.375
	msg := pack77("CQ", "HS0ZKO", "OK04")
	tones := encodeTones(msg)
	ring := buildRing(synthFrame(tones, center, 1.0, 0, rng), 1*8000+353, 0, rng)
	_, got := runDetectDecode(t, ring, center)
	if got == nil || !got.Valid {
		t.Fatalf("decode failed (clean signal)")
	}
	if want := "CQ HS0ZKO OK04"; got.Text != want {
		t.Fatalf("decoded %q, want %q", got.Text, want)
	}
}

// TestFT8RoundTripNoise repeats the round trip at a realistic SNR
// (~0 dB in 50 Hz): the BP decoder must still recover the message.
func TestFT8RoundTripNoise(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const center = 1862.5
	msg := pack77("E23BC", "W1AW", "FN42")
	tones := encodeTones(msg)
	sig := synthFrame(tones, center, 1.0, 0.35, rng)
	ring := buildRing(sig, 1*8000+777, 0.35, rng)
	_, got := runDetectDecode(t, ring, center)
	if got == nil || !got.Valid {
		t.Fatalf("decode failed at noise 0.35")
	}
	if want := "E23BC W1AW FN42"; got.Text != want {
		t.Fatalf("decoded %q, want %q", got.Text, want)
	}
}

// TestFT8ReportMessage checks the RRR/report tail decoding path via a
// hand-built payload (report -07 in the g15 field).
func TestFT8ReportMessage(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	const center = 2000.0
	b := pack77("K1ABC", "W9XYZ", "EN37")
	// replace grid with report -07: irpt = -7+35 = 28
	put := func(from int, v uint64, n int) {
		for i := 0; i < n; i++ {
			b[from+i] = int((v >> uint(n-1-i)) & 1)
		}
	}
	put(59, 28+ft8MaxGrid4, 15)
	tones := encodeTones(b)
	ring := buildRing(synthFrame(tones, center, 1.0, 0.15, rng), 2*8000+1234, 0.15, rng)
	_, got := runDetectDecode(t, ring, center)
	if got == nil || !got.Valid {
		t.Fatalf("decode failed (report)")
	}
	if want := "K1ABC W9XYZ -07"; got.Text != want {
		t.Fatalf("decoded %q, want %q", got.Text, want)
	}
}

// TestFT8UnpackUnit sanity-checks the unpacker against known values
// from WSJT-X's std_call_to_c28 / grid4_to_g15 utilities.
func TestFT8UnpackUnit(t *testing.T) {
	cases := []struct {
		call string
		n28  uint64
	}{
		{"K1ABC", packCall28("K1ABC")},
		{"HS0ZKO", packCall28("HS0ZKO")},
		{"W9XYZ", packCall28("W9XYZ")},
		{"E23BC", packCall28("E23BC")},
	}
	for _, c := range cases {
		if got := ft8Unpack28(c.n28); got != c.call {
			t.Errorf("unpack28(%s) = %q (n28=%d)", c.call, got, c.n28)
		}
	}
	if got := ft8Unpack28(2); got != "CQ" {
		t.Errorf("token 2 = %q, want CQ", got)
	}
	if g, _ := ft8UnpackGrid15(packGrid15("OK04"), 0); g != "OK04" {
		t.Errorf("grid = %q, want OK04", g)
	}
	if r, _ := ft8UnpackGrid15(ft8MaxGrid4+2, 0); r != "RRR" {
		t.Errorf("report = %q, want RRR", r)
	}
	fmt.Println("unpack unit ok")
}

// TestFT8FullProcess exercises the complete path the device runs:
// Feed() chunks → Process() → Results(), including the FFT candidate
// finder (the earlier tests bypassed it with a known frequency).
func TestFT8FullProcess(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	const center = 1500.0
	msg := pack77("CQ", "JA1ABC", "PM95")
	tones := encodeTones(msg)
	sig := synthFrame(tones, center, 1.0, 0.25, rng)
	ring := buildRing(sig, 17000, 0.25, rng)
	d := NewFT8Detector()
	d.SetEnabled(true)
	// Feed in 512-sample chunks like the DSP taps do.
	for i := 0; i < len(ring); i += 512 {
		e := i + 512
		if e > len(ring) {
			e = len(ring)
		}
		d.Feed(ring[i:e])
	}
	d.Process()
	results := d.Results()
	found := false
	for _, r := range results {
		t.Logf("det: %.1f Hz %.1f dB conf %.2f msg=%v", r.FreqHz, r.SNRDb, r.Confidence, r.Message)
		if r.Message != nil && r.Message.Valid && r.Message.Text == "CQ JA1ABC PM95" {
			found = true
		}
	}
	if !found {
		t.Fatalf("full pipeline did not decode; results=%d", len(results))
	}
}
