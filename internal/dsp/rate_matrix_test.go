package dsp

import (
	"math"
	"testing"
)

// TestChannelRateMatrix: for every supported IQ rate the NFM and AM
// chains must produce a channel rate that is an exact integer multiple
// of the 8 kHz audio rate — a truncated audio decimation used to stream
// 9.33 kHz audio into the 8 kHz resampler at IF2 224 kHz (IQ 1.792M),
// pitch-shifting every voice.
func TestChannelRateMatrix(t *testing.T) {
	rates := []int{256_000, 1_024_000, 1_536_000, 1_792_000, 2_048_000, 2_560_000, 2_880_000, 3_200_000}
	for _, iq := range rates {
		SetIQRate(iq)
		for _, m := range []Mode{ModeNFM, ModeAM} {
			c := NewChain(m, nil, nil)
			if c.chRate < c.outRate {
				t.Fatalf("%s @%.3fM: channel rate %d below audio rate %d", m.Name, float64(iq)/1e6, c.chRate, c.outRate)
			}
			if c.chRate%c.outRate != 0 {
				t.Fatalf("%s @%.3fM: channel rate %d not a multiple of %d — auD %d truncates (real audio %g Hz)",
					m.Name, float64(iq)/1e6, c.chRate, c.outRate, c.auD, float64(c.chRate)/float64(c.auD))
			}
			if c.auD*c.outRate != c.chRate {
				t.Fatalf("%s @%.3fM: auD %d × %d ≠ chRate %d", m.Name, float64(iq)/1e6, c.auD, c.outRate, c.chRate)
			}
			if IQRate%IF2Rate != 0 || IF2Rate%8000 != 0 {
				t.Fatalf("IF2 %d not 8k-divisible at IQ %d", IF2Rate, iq)
			}
		}
	}
	// Rates that were already exact keep their previous factors
	// (2.048M → chD 8 / chRate 32k, 256k → chD 4 / chRate 8k).
	SetIQRate(2_048_000)
	if c := NewChain(ModeNFM, nil, nil); c.chD != 8 || c.chRate != 32_000 {
		t.Fatalf("2.048M NFM changed: chD %d chRate %d (was 8/32000)", c.chD, c.chRate)
	}
	SetIQRate(256_000)
	// IF2 is adaptive (÷4 below 640k IQ): 256k → IF2 64k → chD 8 → 8k.
	if c := NewChain(ModeNFM, nil, nil); c.chD != 8 || c.chRate != 8_000 {
		t.Fatalf("256k NFM changed: chD %d chRate %d (was 8/8000)", c.chD, c.chRate)
	}
	// The broken cases now land on exact multiples.
	SetIQRate(1_792_000)
	if c := NewChain(ModeNFM, nil, nil); c.chRate%8000 != 0 {
		t.Fatalf("1.792M NFM still inexact: chRate %d", c.chRate)
	}
}

// TestNFMAudioRateAt1792: end-to-end at the previously broken IQ rate —
// half a second of FM must yield ~4000 audio samples at 8 kHz, not the
// ~4666 a 9.33 kHz stream produced.
func TestNFMAudioRateAt1792(t *testing.T) {
	SetIQRate(1_792_000)
	c := NewChain(ModeNFM, nil, nil)
	const nSec = 0.5
	n := int(nSec * float64(IQRate))
	var out []float32
	const toneHz, dev = 800.0, 2500.0
	phase := 0.0
	for pos := 0; pos < n; pos += 16384 {
		end := pos + 16384
		if end > n {
			end = n
		}
		buf := make([]byte, 2*(end-pos))
		for i := pos; i < end; i++ {
			phase += 2 * math.Pi * dev * math.Sin(2*math.Pi*toneHz*float64(i)/8000.0) / float64(IQRate)
			re := 0.5 * math.Cos(phase)
			im := 0.5 * math.Sin(phase)
			buf[2*(i-pos)] = byte(127.5 + re*127.5)
			buf[2*(i-pos)+1] = byte(127.5 + im*127.5)
		}
		c.Process(buf, &out)
	}
	want := int(nSec * 8000)
	if d := len(out) - want; d < -100 || d > 100 {
		t.Fatalf("audio sample count %d ≠ %d (±100) — chain output rate is wrong (auD truncation)", len(out), want)
	}
}
