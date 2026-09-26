package dsp

import (
	"math"
	"testing"
)

// genFMOffset produces nSec of u8 IQ carrying an FM tone whose carrier
// sits offsetHz from the LO (an LO/ppm error seen from the chain's
// point of view).
func genFMOffset(nSec float64, toneHz, dev, offsetHz float64) []byte {
	n := int(nSec * float64(IQRate))
	buf := make([]byte, 2*n)
	phase := 0.0
	for i := 0; i < n; i++ {
		t := float64(i) / float64(IQRate)
		phase += 2 * math.Pi * (dev*math.Sin(2*math.Pi*toneHz*t) + offsetHz) / float64(IQRate)
		re := 0.5 * math.Cos(phase)
		im := 0.5 * math.Sin(phase)
		buf[2*i] = byte(127.5 + re*127.5)
		buf[2*i+1] = byte(127.5 + im*127.5)
	}
	return buf
}

// TestNFMOffsetDCRemoval: regression for the "NFM sounds muffled even
// at 20 kHz bandwidth" report — a 3 kHz LO/ppm offset puts a DC bias
// of 3 kHz on the discriminator, the old path clamped it against
// ±2.5 kHz scaling (audio smashed into the rail). With the DC tracker
// the audio comes out centred, unclamped, and carrying the tone.
func TestNFMOffsetDCRemoval(t *testing.T) {
	SetIQRate(2_048_000)
	c := NewChain(ModeNFM, nil, nil)
	var out []float32
	data := genFMOffset(1.5, 800, 2500, 3000)
	for pos := 0; pos < len(data); pos += 16384 {
		end := pos + 16384
		if end > len(data) {
			end = len(data)
		}
		c.Process(data[pos:end], &out)
	}
	// Judge the last 0.5 s (after the ~1 s DC settle).
	seg := out[len(out)-4000:]
	mean := 0.0
	peak := 0.0
	for _, v := range seg {
		f := float64(v)
		mean += f
		if a := math.Abs(f); a > peak {
			peak = a
		}
	}
	mean /= float64(len(seg))
	if math.Abs(mean) > 0.05 {
		t.Fatalf("audio carries DC %.3f — LO offset not removed", mean)
	}
	if peak > 0.99 {
		t.Fatalf("audio clamped (peak %.3f) — offset distortion", peak)
	}
	// The 800 Hz tone must dominate the segment.
	w := 2 * math.Pi * 800 / 8000
	var sre, sim, e float64
	for i, v := range seg {
		sre += float64(v) * math.Cos(w*float64(i))
		sim += float64(v) * math.Sin(w*float64(i))
		e += float64(v) * float64(v)
	}
	tonePower := (sre*sre + sim*sim) / float64(len(seg)*len(seg))
	totalPower := e / float64(len(seg))
	if tonePower < 0.25*totalPower {
		t.Fatalf("800 Hz tone lost: tone %.5f vs total %.5f", tonePower, totalPower)
	}
	t.Logf("mean=%.4f peak=%.3f toneFrac=%.2f", mean, peak, tonePower/totalPower)
}
