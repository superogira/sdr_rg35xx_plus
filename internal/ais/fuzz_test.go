package ais

import (
	"math/rand"
	"testing"
)

// Feed the frame decoder random bit streams: with a correct X.25 FCS
// gate the false-accept rate must be ~2^-16 per attempt window.
func TestDecoderRandomNoFalseFrames(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	const trials = 2000
	accepted := 0
	for i := 0; i < trials; i++ {
		d := &frameDecoder{emit: func(p []byte, ch int, levelDb float64) { accepted++ }}
		d.reset()
		// ~600 random transition bits per trial
		for k := 0; k < 600; k++ {
			var b byte
			if rng.Intn(2) == 1 {
				b = 1
			}
			d.run(b)
		}
	}
	t.Logf("accepted %d of %d random trials", accepted, trials)
	if accepted > trials/100 { // wildly generous bound
		t.Fatalf("too many false frames: %d/%d", accepted, trials)
	}
}
