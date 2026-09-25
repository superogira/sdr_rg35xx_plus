package dsp

import (
	"math"
	"math/rand"
	"testing"
)

// TestFT8DecodesInAMMode: the FT8 monitor branch must feed the detector
// while the audio path is AM. Regression for the starved-detector bug:
// the feed used to live only in processSSB, so enabling FT8 while
// listening in AM (or NFM/WFM/wide-SSB) produced zero decodes — and AM's
// envelope detector flattens the constant-envelope FSK tones to DC, so
// the mode's own audio could never carry them anyway.
func TestFT8DecodesInAMMode(t *testing.T) {
	SetIQRate(256_000) // the mobile setting: IF2 64k, ft8DecD 8
	chain := NewChain(ModeAM, nil, nil)
	det := NewFT8Detector()
	det.SetEnabled(true)
	chain.SetFT8Detector(det)

	rng := rand.New(rand.NewSource(5))
	tones := encodeTones(pack77("CQ", "HS0ZKO", "OK04"))

	// Render the frame directly as complex baseband at the capture rate:
	// group at +1500 Hz, continuous phase across symbols (same convention
	// as synthFrame), u8 IQ.
	const amp = 0.45
	const groupHz = 1500.0
	const leadSec = 0.4
	const tailSec = 0.4
	total := int((leadSec + float64(FT8FrameSamp)/float64(FT8AudioRate) + tailSec) * float64(IQRate))

	// tone frequency per sample index
	freqAt := func(i int) float64 {
		off := float64(i)/float64(IQRate) - leadSec // seconds into frame
		if off < 0 || off >= float64(FT8FrameSamp)/float64(FT8AudioRate) {
			return groupHz // silence handled below by amp envelope
		}
		sym := int(off * float64(FT8AudioRate) / float64(FT8SymSamples))
		if sym >= len(tones) {
			sym = len(tones) - 1
		}
		return groupHz + (float64(tones[sym])-3.5)*FT8ToneHz
	}

	phase := rng.Float64() * 2 * math.Pi
	var out []float32
	for pos := 0; pos < total; pos += 16384 {
		end := pos + 16384
		if end > total {
			end = total
		}
		buf := make([]byte, 2*(end-pos))
		for i := pos; i < end; i++ {
			tt := float64(i) / float64(IQRate)
			inFrame := tt >= leadSec && tt < leadSec+float64(FT8FrameSamp)/float64(FT8AudioRate)
			a := amp
			if !inFrame {
				a = 0 // idle carrier between frames is fine for AM
			}
			w := 2 * math.Pi * freqAt(i) / float64(IQRate)
			re := a * math.Cos(phase)
			im := a * math.Sin(phase)
			phase += w
			buf[2*(i-pos)] = byte(127.5 + re*127.5)
			buf[2*(i-pos)+1] = byte(127.5 + im*127.5)
		}
		chain.Process(buf, &out)
	}

	det.Process()
	found, freq := false, 0.0
	for _, m := range det.TakeMessages() {
		if m.Text == "CQ HS0ZKO OK04" && m.Valid {
			found = true
			freq = m.FreqHz
		}
	}
	if !found {
		t.Fatalf("AM-mode FT8 decode failed — monitor branch starved?")
	}
	t.Logf("decoded in AM mode at %.0f Hz", freq)
	if freq < 1300 || freq > 1700 {
		t.Errorf("decoded freq %.0f Hz, want ~1500 (band alignment off)", freq)
	}
}
