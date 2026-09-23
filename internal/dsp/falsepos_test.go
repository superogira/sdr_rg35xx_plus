package dsp

import (
	"math/rand"
	"testing"
)

// TestNoFalsePositives feeds pure noise and counts "decoded" messages
// across many scans — the lowered sync threshold must not conjure
// phantoms (CRC gates, but the rate needs measuring).
func TestNoFalsePositives(t *testing.T) {
	phantoms := 0
	scans := 0
	for seed := int64(200); seed < 220; seed++ {
		rng := rand.New(rand.NewSource(seed))
		d := NewFT8Detector()
		d.SetEnabled(true)
		// 15 s of noise fed in chunks
		total := 0
		for total < 15*FT8AudioRate {
			chunk := make([]float64, 512)
			for i := range chunk {
				chunk[i] = 1.0 * rng.NormFloat64()
			}
			d.Feed(chunk)
			total += 512
		}
		d.Process()
		scans++
		for _, m := range d.TakeMessages() {
			if m.Valid {
				phantoms++
				t.Logf("PHANTOM seed %d: %q", seed, m.Text)
			}
		}
	}
	t.Logf("scans=%d phantoms=%d", scans, phantoms)
	if phantoms > 0 {
		t.Errorf("false positives on pure noise: %d/%d", phantoms, scans)
	}
}
