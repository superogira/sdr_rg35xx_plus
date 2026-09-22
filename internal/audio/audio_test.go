package audio

import (
	"math"
	"testing"

	"sdr35/internal/dsp"
)

type bufPipe struct{ b []byte }

func (p *bufPipe) Write(b []byte) (int, error) { p.b = append(p.b, b...); return len(b), nil }
func (p *bufPipe) Close() error                { return nil }

// TestResamplerRateAndTone feeds a 1 kHz tone at dsp.AudioRate through
// WriteAudio and checks the output frame count and that the tone survives
// the 64k→48k conversion.
func TestResamplerRateAndTone(t *testing.T) {
	pipe := &bufPipe{}
	o := &Output{pending: make([]byte, 0, chunkFrames*frameBytes), stdin: pipe}
	o.SetInputRate(dsp.AudioRate)

	seconds := 2.0
	n := int(seconds * float64(dsp.AudioRate))
	tone := make([]float32, n)
	for i := range tone {
		tone[i] = float32(0.5 * math.Sin(2*math.Pi*1000*float64(i)/float64(dsp.AudioRate)))
	}
	o.WriteAudio(tone)

	total := len(pipe.b) + len(o.pending)
	frames := total / frameBytes
	want := int(seconds * SampleRate)
	if d := frames - want; d < -3 || d > 3 {
		t.Fatalf("frame count %d, want ~%d", frames, want)
	}

	// Decode mono (left channel) and find the dominant tone.
	all := append(append([]byte(nil), pipe.b...), o.pending...)
	mono := make([]float64, frames)
	for i := range mono {
		off := i * frameBytes
		v := int16(all[off]) | int16(all[off+1])<<8
		mono[i] = float64(v) / 32767
	}
	nfft := 1
	for nfft*2 <= frames {
		nfft *= 2
	}
	if nfft < 1024 {
		t.Fatal("too few frames for spectral check")
	}
	re := make([]float64, nfft)
	im := make([]float64, nfft)
	for i := 0; i < nfft; i++ {
		re[i] = mono[i]
	}
	dsp.HannWindow(re, im)
	dsp.FFT(re, im)
	best, bestMag := 0, 0.0
	for i := 1; i < nfft/2; i++ {
		m := math.Hypot(re[i], im[i])
		if m > bestMag {
			best, bestMag = i, m
		}
	}
	f := float64(best) * SampleRate / float64(nfft)
	if math.Abs(f-1000) > 40 {
		t.Errorf("dominant tone %.1f Hz after resample, want 1000 ±40", f)
	}
	t.Logf("resampler: %d frames (%.2fs), tone %.1f Hz", frames,
		float64(frames)/SampleRate, f)
}
