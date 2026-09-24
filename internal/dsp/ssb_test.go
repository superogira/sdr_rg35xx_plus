package dsp

import (
	"math"
	"testing"
)

// genToneIQ synthesizes IQ at IQRate containing a single complex tone at
// +toneHz (positive = USB side) or negative (LSB side).
func genToneIQ(seconds float64, toneHz, amp float64) []byte {
	n := int(seconds * float64(IQRate))
	out := make([]byte, 2*n)
	phase := 0.0
	for i := 0; i < n; i++ {
		phase += 2 * math.Pi * toneHz / float64(IQRate)
		re := amp * math.Cos(phase)
		im := amp * math.Sin(phase)
		out[2*i] = byte(math.Round(re*119 + 127.5))
		out[2*i+1] = byte(math.Round(im*119 + 127.5))
	}
	return out
}

func rmsF(samples []float32) float64 {
	var s float64
	for _, v := range samples {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(samples)))
}

// TestSSBSelectsSideband: a +1 kHz tone (USB energy) must appear in USB
// audio and be absent from LSB audio, and vice versa.
func TestSSBSelectsSideband(t *testing.T) {
	cases := []struct {
		toneHz float64
		mode   Mode
		want   float64 // expected relative output
	}{
		{1000, ModeUSB, 1},
		{1000, ModeLSB, 0},
		{-1000, ModeUSB, 0},
		{-1000, ModeLSB, 1},
		{700, ModeCW, 1},
		{-700, ModeCW, 0},
	}
	for _, tc := range cases {
		iq := genToneIQ(0.5, tc.toneHz, 0.5)
		ch := NewChain(tc.mode, nil, nil)
		var audio []float32
		processChunked(ch, iq, &audio)
		audio = audio[SSBRate/4:] // skip settling
		r := rmsF(audio)
		if tc.want == 1 && r < 0.05 {
			t.Errorf("%s tone %+v Hz: RMS %.3f, expected audible", tc.mode.Name, tc.toneHz, r)
		}
		if tc.want == 0 && r > 0.02 {
			t.Errorf("%s tone %+v Hz: RMS %.3f, expected rejected", tc.mode.Name, tc.toneHz, r)
		}
		t.Logf("%-3s tone %+6.0f Hz → RMS %.3f (want %v)", tc.mode.Name, tc.toneHz, r, tc.want)
	}
}

// TestSSBAudioRate: the SSB branch must produce SSBRate samples per
// second of input.
func TestSSBAudioRate(t *testing.T) {
	iq := genToneIQ(0.5, 1000, 0.5)
	ch := NewChain(ModeUSB, nil, nil)
	var audio []float32
	processChunked(ch, iq, &audio)
	want := int(0.5 * float64(SSBRate) * 0.95)
	if len(audio) < want {
		t.Fatalf("SSB audio length %d, want ≥%d", len(audio), want)
	}
}

// TestModeCycling covers the full mode list.
func TestModeCycling(t *testing.T) {
	m := ModeNFM
	seen := map[string]bool{}
	for range ModeList {
		m = NextMode(m)
		seen[m.Name] = true
	}
	if len(seen) != len(ModeList) {
		t.Fatalf("cycling visited %v, want all %d modes", seen, len(ModeList))
	}
	// The LAST mode in the list wraps back to the first.
	last := ModeList[len(ModeList)-1]
	if NextMode(last).Name != ModeList[0].Name {
		t.Errorf("%s should wrap to %s, got %s", last.Name, ModeList[0].Name, NextMode(last).Name)
	}
}
