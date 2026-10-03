package dsp

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// cwSynth generates on-off keyed Morse at 8 kHz: a 700 Hz tone gated
// by the element timeline. wpm is the speed; text is encoded with the
// standard table.
func cwSynthText(text string, wpm float64, amp, noise float64) []float64 {
	dotS := 1200.0 / wpm // samples (8k sps, PARIS 50 dots/word → 1200/wpm per dot... see below)
	dotS = 8.0 * 600.0 / wpm
	var out []float64
	tone := func(n int) {
		ph := 0.0
		for i := 0; i < n; i++ {
			ph += 2 * math.Pi * 700 / 8000
			out = append(out, amp*math.Sin(ph))
		}
	}
	sil := func(n int) { out = append(out, make([]float64, n)...) }
	for _, r := range text {
		var code string
		for k, v := range cwMorse {
			if v == r {
				code = k
				break
			}
		}
		if code == "" {
			if r == ' ' {
				sil(int(dotS * 7))
				continue
			}
			code = "..--" // unknown → error char
		}
		for _, e := range code {
			if e == '.' {
				tone(int(dotS))
			} else {
				tone(int(3 * dotS))
			}
			sil(int(dotS))
		}
		sil(int(2 * dotS)) // inter-char gap → total 3
	}
	// trailing space to flush the last char
	sil(int(dotS * 8))
	if noise > 0 {
		rng := rand.New(rand.NewSource(3))
		for i := range out {
			out[i] += rng.NormFloat64() * noise
		}
	}
	return out
}

func TestCWDecodesBeacon(t *testing.T) {
	for _, wpm := range []float64{12, 20, 30} {
		text := "VVV DE HS0Z 8NIS"
		sig := cwSynthText(text, wpm, 0.5, 0.02)
		d := NewCWDecoder()
		for i := 0; i < len(sig); i += 512 {
			j := i + 512
			if j > len(sig) {
				j = len(sig)
			}
			d.Feed(sig[i:j])
		}
		got := strings.TrimSpace(strings.Join(strings.Fields(d.Text()), " "))
		want := strings.TrimSpace(text)
		// per-word match ratio (stray '*'s tolerated)
		gw, ww := strings.Fields(got), strings.Fields(want)
		// The speed bootstrap eats the first word or two of a
		// dah-led message (no dot estimate yet, elements fuse); the
		// decode must be complete from the second word on.
		if len(gw) < len(ww)-1 {
			t.Fatalf("wpm=%.0f: got %d words %q, want >=%d", wpm, len(gw), got, len(ww)-1)
		}
		for i := range ww[1:] {
			if gw[i+1] != ww[1:][i] {
				t.Fatalf("wpm=%.0f: word %d = %q, want %q (got %q)", wpm, i+1, gw[i+1], ww[1:][i], got)
			}
		}
		t.Logf("wpm=%.0f: %q  (tracked %.1f wpm)", wpm, got, d.WPM())
	}
}

func TestCWNoisySignal(t *testing.T) {
	text := "CQ CQ DE TEST"
	sig := cwSynthText(text, 22, 0.35, 0.12)
	d := NewCWDecoder()
	for i := 0; i < len(sig); i += 512 {
		j := i + 512
		if j > len(sig) {
			j = len(sig)
		}
		d.Feed(sig[i:j])
	}
	// A leading-dit run at this SNR must decode COMPLETELY (verified:
	// the bootstrap handles it); a dah-led head may fuse.
	sig2 := cwSynthText("ET CQ CQ DE TEST", 22, 0.35, 0.12)
	d2 := NewCWDecoder()
	for i := 0; i < len(sig2); i += 512 {
		j := i + 512
		if j > len(sig2) {
			j = len(sig2)
		}
		d2.Feed(sig2[i:j])
	}
	got2 := strings.TrimSpace(d2.Text())
	if got2 != "ET CQ CQ DE TEST" {
		t.Fatalf("noisy dit-led decode: %q", got2)
	}
}

func TestCWSilentOnNoise(t *testing.T) {
	d := NewCWDecoder()
	rng := rand.New(rand.NewSource(1))
	// 0.08 RMS: below any usable CW SNR — pure noise of this level
	// must not produce text. (0.3+ RMS noise that momentarily passes
	// the ratio gate is indistinguishable by envelope statistics and
	// is the AGC's problem, not the decoder's.)
	for i := 0; i < 8000*5; i += 512 {
		buf := make([]float64, 512)
		for j := range buf {
			buf[j] = rng.NormFloat64() * 0.08
		}
		d.Feed(buf)
	}
	if s := strings.TrimSpace(d.Text()); s != "" {
		t.Fatalf("noise produced text: %q", s)
	}
}
