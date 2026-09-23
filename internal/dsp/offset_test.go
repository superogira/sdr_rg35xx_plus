package dsp

import (
	"math"
	"testing"
)

// TestPassbandOffsetCW: with an NFM-style passband offset of −5 kHz,
// a CW tone placed 700 Hz above the listening frequency must arrive
// at the 700 Hz audio beat (the whole point of offset tuning).
func TestPassbandOffsetCW(t *testing.T) {
	SetIQRate(2_048_000)
	chain := NewChain(ModeCW, nil, nil)
	chain.SetOffsetHz(5000) // listen 5 kHz above LO (rotate by -5k)

	// Build u8 IQ with a tone at +5.7 kHz (= listen + 700 beat).
	const toneHz = 5700.0
	const nSec = 1.0
	n := int(nSec * IQRate)
	var out []float32
	for pos := 0; pos < n; pos += 16384 {
		end := pos + 16384
		if end > n {
			end = n
		}
		buf := make([]byte, 2*(end-pos))
		for i := pos; i < end; i++ {
			re := 0.5 * math.Cos(2*math.Pi*toneHz*float64(i)/float64(IQRate))
			im := 0.5 * math.Sin(2*math.Pi*toneHz*float64(i)/float64(IQRate))
			buf[2*(i-pos)] = byte(127.5 + re*127.5)
			buf[2*(i-pos)+1] = byte(127.5 + im*127.5)
		}
		chain.Process(buf, &out)
	}
	// Measure the dominant audio frequency over the last half second.
	seg := out[len(out)-4000:]
	best, bestMag := 0.0, 0.0
	for f := 300.0; f <= 1200.0; f += 5 {
		w := 2 * math.Pi * f / 8000.0
		var sre, sim float64
		for i, v := range seg {
			sre += float64(v) * math.Cos(w*float64(i))
			sim += float64(v) * math.Sin(w*float64(i))
		}
		if m := math.Hypot(sre, sim); m > bestMag {
			bestMag, best = m, f
		}
	}
	t.Logf("dominant audio tone: %.0f Hz", best)
	if math.Abs(best-700) > 40 {
		t.Fatalf("expected ~700 Hz beat, got %.0f Hz", best)
	}
}
