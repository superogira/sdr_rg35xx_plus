package dsp

import (
	"math/rand"
	"testing"
)

// findFloor reports the deepest noise level (worst case of a few
// seeds) at which the message still decodes.
func findFloor(t *testing.T, maxNoise float64) float64 {
	t.Helper()
	payload := pack77("K1ABC", "W9XYZ", "EN37")
	tones := encodeTones(payload)
	last := 0.0
	for _, noise := range []float64{8.0, 8.5, 9.0, 9.5, 10.0} {
		if noise > maxNoise {
			break
		}
		okAll := true
		for seed := int64(60); seed < 66; seed++ {
			rng := rand.New(rand.NewSource(seed))
			sig := synthFrame(tones, 1800.0, 1.0, noise, rng)
			d := NewFT8Detector()
			d.SetEnabled(true)
			feedRing(d, sig, 2*FT8SymSamples, noise, rng)
			d.Process()
			found := false
			for _, m := range d.TakeMessages() {
				if m.Text == "K1ABC W9XYZ EN37" {
					found = true
				}
			}
			if !found {
				okAll = false
				break
			}
		}
		t.Logf("noise %.1f: %v", noise, map[bool]string{true: "decode ok (6/6 seeds)", false: "FAILED"}[okAll])
		if okAll {
			last = noise
		} else {
			break
		}
	}
	return last
}

func TestDecodeFloor(t *testing.T) {
	t.Logf("floor = noise %.1f", findFloor(t, 10.0))
}
