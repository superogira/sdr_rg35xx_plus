package dsp

import (
	"math"
	"math/rand"
	"testing"
)

func toneMix(n int, fs, f0, amp float64, noise float64, seed int64) []float64 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float64, n)
	for i := range out {
		out[i] = amp*math.Sin(2*math.Pi*f0*float64(i)/fs) + noise*r.NormFloat64()
	}
	return out
}

func bandPower(x []float64, fs, f float64) float64 {
	w := 2 * math.Pi * f / fs
	var sre, sim float64
	for i, v := range x {
		sre += v * math.Cos(w*float64(i))
		sim += v * math.Sin(w*float64(i))
	}
	return (sre*sre + sim*sim) / float64(len(x)*len(x))
}

// TestNoiseReductionImprovesSNR: a 700 Hz tone buried in noise must
// come out with a markedly better tone-to-broadband-power ratio, and
// the tone itself must survive.
func TestNoiseReductionImprovesSNR(t *testing.T) {
	const fs = 8000.0
	x := toneMix(48000, fs, 700, 0.3, 0.5, 7)

	raw := append([]float64(nil), x...)
	nr := NewNoiseReduction()
	nr.SetLevel(6)
	var out []float64
	for _, v := range raw {
		out = append(out, nr.Step(v))
	}
	// Judge the adapted tail (last 24k samples).
	tail := out[len(out)-24000:]
	rawTail := raw[len(raw)-24000:]

	tone := bandPower(tail, fs, 700)
	rawTone := bandPower(rawTail, fs, 700)
	var e, rawE float64
	for i := range tail {
		e += tail[i] * tail[i]
		rawE += rawTail[i] * rawTail[i]
	}
	total := e / float64(len(tail))
	rawTotal := rawE / float64(len(rawTail))
	snrraw := rawTone / (rawTotal - rawTone)
	snr := tone / (total - tone)
	t.Logf("tone %.5f (raw %.5f), snr %.2f (raw %.2f)", tone, rawTone, snr, snrraw)
	if tone < 0.4*rawTone {
		t.Fatalf("NR destroyed the tone (%.5f vs raw %.5f)", tone, rawTone)
	}
	if snr < 3*snrraw {
		t.Fatalf("NR did not improve SNR meaningfully: %.2f vs raw %.2f", snr, snrraw)
	}
	// Off passes audio through untouched.
	nr.SetLevel(0)
	if y := nr.Step(0.123); y != 0.123 {
		t.Fatalf("NR level 0 must bypass, got %v", y)
	}
}

// TestAudioFiltersShape: HP kills a low tone, LP kills a high tone,
// and the mid-band tone survives both.
func TestAudioFiltersShape(t *testing.T) {
	const fs = 8000.0
	p := newAudProc()
	p.SetAudioFilters(300, 2800, fs) // HP 300, LP 2800

	for _, c := range []struct {
		f     float64
		amp   float64
		gain  float64 // expected magnitude ratio vs no filter
		label string
	}{
		// bandPower of a pure amp-A sine measures (A/2)², so an untouched
		// tone reads 0.5 on the gain scale below.
		{80, 0.5, 0.12, "HP kills 80 Hz"},
		{3800, 0.5, 0.12, "LP kills 3.8 kHz"},
		{1000, 0.5, 0.44, "1 kHz survives"},
	} {
		x := toneMix(16000, fs, c.f, c.amp, 0, 3)
		p.hp.reset()
		p.lp.reset()
		var out []float64
		for _, v := range x {
			out = append(out, p.step(v))
		}
		tail := out[8000:]
		got := math.Sqrt(bandPower(tail, fs, c.f)) / c.amp
		t.Logf("%s: gain %.2f", c.label, got)
		if c.f == 1000 {
			if got < c.gain {
				t.Fatalf("%s: gain %.2f (want >= %.2f)", c.label, got, c.gain)
			}
		} else if got > c.gain {
			t.Fatalf("%s: gain %.2f (want <= %.2f)", c.label, got, c.gain)
		}
	}
	// Off = bypass.
	p.SetAudioFilters(0, 0, fs)
	p.hp.reset()
	p.lp.reset()
	if y := p.step(0.5); y != 0.5 {
		t.Fatalf("filters off must bypass, got %v", y)
	}
}
