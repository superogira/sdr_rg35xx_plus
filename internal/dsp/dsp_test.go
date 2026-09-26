package dsp

import (
	"math"
	"math/rand"
	"testing"
)

func TestDesignLowpassDCGain(t *testing.T) {
	for _, tc := range []struct {
		taps      int
		cut, rate float64
	}{
		{63, 108000, float64(IQRate)},
		{47, 15000, float64(IF2Rate)},
		{191, 2800, float64(IF2Rate)},
	} {
		h := DesignLowpass(tc.taps, tc.cut, tc.rate)
		var sum float64
		for _, v := range h {
			sum += v
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("taps=%d cut=%v: DC gain %v, want 1", tc.taps, tc.cut, sum)
		}
	}
}

func TestFFTSingleBin(t *testing.T) {
	n := 512
	re := make([]float64, n)
	im := make([]float64, n)
	// A complex exponential at bin 12 transforms to a single spike.
	for i := 0; i < n; i++ {
		ang := 2 * math.Pi * 12 * float64(i) / float64(n)
		re[i] = math.Cos(ang)
		im[i] = math.Sin(ang)
	}
	FFT(re, im)
	for i := range re {
		mag := math.Hypot(re[i], im[i])
		if i == 12 {
			if mag < float64(n)*0.99 {
				t.Errorf("bin 12 magnitude %v, want ~%d", mag, n)
			}
		} else if mag > float64(n)*0.01 {
			t.Errorf("bin %d magnitude %v, want ~0", i, mag)
		}
	}
}

// genFM synthesizes an FM-modulated IQ stream as rtl_tcp would deliver it:
// u8 interleaved, carrier at center, plus a DC offset to exercise the DC
// blocker.
func genFM(seconds float64, toneHz, deviation, amplitude float64) []byte {
	n := int(seconds * float64(IQRate))
	out := make([]byte, 2*n)
	phase := 0.0
	for i := 0; i < n; i++ {
		m := math.Sin(2 * math.Pi * toneHz * float64(i) / float64(IQRate))
		phase += 2 * math.Pi * deviation * m / float64(IQRate)
		z := complex(amplitude*math.Cos(phase), amplitude*math.Sin(phase))
		// +8 counts of DC on both rails like the real dongle.
		out[2*i] = byte(math.Round(real(z)*119 + 127.5 + 8))
		out[2*i+1] = byte(math.Round(imag(z)*119 + 127.5 + 8))
	}
	return out
}

// dominantTone returns the frequency of the largest spectral peak of the
// middle chunk of samples, in Hz, plus its magnitude.
func dominantTone(samples []float32, rate int) (float64, float64) {
	n := 1
	for n*2 <= len(samples)/2 && n < 8192 {
		n *= 2
	}
	re := make([]float64, n)
	im := make([]float64, n)
	off := len(samples)/2 - n/2
	for i := 0; i < n; i++ {
		re[i] = float64(samples[off+i])
	}
	HannWindow(re, im)
	FFT(re, im)
	best, bestMag := 0, 0.0
	for i := 1; i < n/2; i++ {
		m := math.Hypot(re[i], im[i])
		if m > bestMag {
			best, bestMag = i, m
		}
	}
	return float64(best) * float64(rate) / float64(n), bestMag
}

func TestChainRecoversWFMTone(t *testing.T) {
	const tone = 1000.0
	iq := genFM(0.5, tone, 40000, 0.8)
	ch := NewChain(ModeWFM, nil, nil)
	var audio []float32
	ch.Process(iq, &audio)
	want := int(0.5 * float64(ch.OutRate()))
	if len(audio) < want*95/100 {
		t.Fatalf("audio length %d, want ~%d", len(audio), want)
	}
	// Discard the first 100 ms (filter settling).
	audio = audio[AudioRate/10:]
	var sumSq float64
	peak := float64(0)
	for _, s := range audio {
		v := float64(s)
		sumSq += v * v
		if math.Abs(v) > peak {
			peak = math.Abs(v)
		}
	}
	rms := math.Sqrt(sumSq / float64(len(audio)))
	if rms < 0.01 {
		t.Fatalf("RMS %v too small — demod produced silence", rms)
	}
	f, mag := dominantTone(audio, ch.OutRate())
	if math.Abs(f-tone) > 60 {
		t.Errorf("dominant tone %v Hz, want %v ±60", f, tone)
	}
	if peak > 1.0 {
		t.Errorf("peak %v exceeds full scale", peak)
	}
	t.Logf("WFM: rms=%.3f peak=%.3f tone=%.1fHz mag=%.1f", rms, peak, f, mag)
}

// processChunked feeds IQ through the chain in production-sized blocks
// (64 KB ≈ 16 ms at IQRate), the way the radio loop delivers TCP reads.
func processChunked(ch *Chain, iq []byte, audio *[]float32) {
	const size = 65536
	for off := 0; off < len(iq); off += size {
		end := off + size
		if end > len(iq) {
			end = len(iq)
		}
		ch.Process(iq[off:end], audio)
	}
}

func TestChainRecoversNFMTone(t *testing.T) {
	const tone = 800.0
	// Realistic sequence: idle noise first so the squelch floor can learn
	// what noise looks like, then the signal appears (like a repeater
	// keying up).
	noise := genNoise(0.4, 0.004)
	sig := genFM(0.5, tone, 2500, 0.5)
	iq := append(noise, sig...)
	ch := NewChain(ModeNFM, nil, nil)
	var audio []float32
	processChunked(ch, iq, &audio)
	// Analyze the on-air part only (skip the noise pre-roll and filter
	// settling: 0.4s + 0.1s).
	start := (4 + 1) * ch.OutRate() / 10
	if start >= len(audio) {
		t.Fatalf("audio too short: %d", len(audio))
	}
	audio = audio[start:]
	if !ch.SquelchOpen() {
		t.Fatalf("squelch did not open on a strong signal after idle noise")
	}
	var sumSq float64
	for _, s := range audio {
		sumSq += float64(s) * float64(s)
	}
	rms := math.Sqrt(sumSq / float64(len(audio)))
	if rms < 0.001 {
		t.Fatalf("RMS %v too small — squelch never opened on a strong signal", rms)
	}
	f, _ := dominantTone(audio, ch.OutRate())
	if math.Abs(f-tone) > 60 {
		t.Errorf("dominant tone %v Hz, want %v ±60", f, tone)
	}
	t.Logf("NFM: rms=%.3f tone=%.1fHz", rms, f)
}

func TestSquelchCycle(t *testing.T) {
	ch := NewChain(ModeNFM, nil, nil)
	ch.SetSquelchDb(-30)
	var audio []float32
	// 0.3s noise → squelch must be closed and audio silent.
	processChunked(ch, genNoise(0.3, 0.004), &audio)
	if ch.SquelchOpen() {
		t.Fatal("squelch open on pure noise")
	}
	// 0.3s strong signal → open, audio flows.
	nSilent := len(audio)
	processChunked(ch, genFM(0.3, 800, 2500, 0.5), &audio)
	if !ch.SquelchOpen() {
		t.Fatal("squelch closed on strong signal")
	}
	onAir := audio[nSilent:]
	var sumSq float64
	for _, s := range onAir {
		sumSq += float64(s) * float64(s)
	}
	if rms := math.Sqrt(sumSq / float64(len(onAir))); rms < 0.05 {
		t.Fatalf("audio RMS %.3f while squelch open", rms)
	}
	// back to noise → closes again (hysteresis; the level meter releases
	// slowly, so the close lands after ~1.5 s — a squelch hang).
	nOpen := len(audio)
	processChunked(ch, genNoise(2.5, 0.004), &audio)
	if ch.SquelchOpen() {
		t.Fatal("squelch stayed open after signal left")
	}
	tail := audio[nOpen:]
	// Skip the hang period — audio legitimately flows while the meter
	// releases; silence is required only after it settles.
	quietFrom := len(tail) - ch.OutRate()/2
	if quietFrom < 0 {
		quietFrom = 0
	}
	tail = tail[quietFrom:]
	sumSq = 0
	for _, s := range tail {
		sumSq += float64(s) * float64(s)
	}
	if rms := math.Sqrt(sumSq / float64(len(tail))); rms > 0.02 {
		t.Fatalf("audio RMS %.3f after squelch closed (should be silent)", rms)
	}
}

// genNoise produces idle-channel IQ at the given RMS amplitude per rail
// (gaussian, before u8 quantization).
func genNoise(seconds float64, amp float64) []byte {
	n := int(seconds * float64(IQRate))
	out := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		out[2*i] = byte(clampq(127.5 + rand.NormFloat64()*amp*119))
		out[2*i+1] = byte(clampq(127.5 + rand.NormFloat64()*amp*119))
	}
	return out
}

func clampq(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func TestSpectrumTap(t *testing.T) {
	tap := NewSpectrumTap()
	var snap [TapLen]complex128
	if g := tap.Snapshot(snap[:]); g != 0 {
		t.Errorf("gen %v on fresh tap, want 0", g)
	}
	block := make([]complex128, TapLen*2)
	for i := range block {
		block[i] = complex(float64(i), 0)
	}
	tap.Push(block)
	if g := tap.Snapshot(snap[:]); g != 1 {
		t.Fatalf("gen %v, want 1", g)
	}
	if snap[0] != complex(float64(TapLen), 0) {
		t.Errorf("snap[0]=%v, want %d (newest half)", snap[0], TapLen)
	}
}
