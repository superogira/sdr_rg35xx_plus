package dsp

import (
	"math"
	"testing"
)

// TestAGCLiftsQuietSSB: a quiet USB tone must be lifted near the target
// level, and a loud one must not clip.
func TestAGCLiftsQuietSSB(t *testing.T) {
	// Quiet signal: amplitude 0.03 (≈ -30 dBFS) — typical weak sideband.
	iq := genToneIQ(1.0, 1000, 0.03)
	ch := NewChain(ModeUSB, nil, nil)
	var audio []float32
	processChunked(ch, iq, &audio)
	audio = audio[SSBRate/2:] // let the AGC settle
	var sum float64
	peak := 0.0
	for _, v := range audio {
		sum += float64(v) * float64(v)
		if math.Abs(float64(v)) > peak {
			peak = math.Abs(float64(v))
		}
	}
	rms := math.Sqrt(sum / float64(len(audio)))
	if rms < 0.1 {
		t.Errorf("quiet SSB still too soft after AGC: RMS %.3f", rms)
	}
	if peak > 1.0 {
		t.Errorf("AGC let the signal clip: peak %.3f", peak)
	}
	t.Logf("quiet USB: input amp 0.03 → audio RMS %.3f peak %.3f", rms, peak)

	// Loud signal: amplitude 0.6 must stay controlled, not clipped away.
	iq2 := genToneIQ(1.0, 1000, 0.6)
	ch2 := NewChain(ModeUSB, nil, nil)
	var audio2 []float32
	processChunked(ch2, iq2, &audio2)
	audio2 = audio2[SSBRate/2:]
	sum = 0
	peak = 0
	for _, v := range audio2 {
		sum += float64(v) * float64(v)
		if math.Abs(float64(v)) > peak {
			peak = math.Abs(float64(v))
		}
	}
	rms = math.Sqrt(sum / float64(len(audio2)))
	if rms > 0.8 {
		t.Errorf("loud SSB over-driven: RMS %.3f", rms)
	}
	t.Logf("loud USB: input amp 0.6 → audio RMS %.3f peak %.3f", rms, peak)
}
