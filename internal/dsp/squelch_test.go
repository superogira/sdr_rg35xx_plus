package dsp

import (
	"math/rand"
	"testing"
)

// feedNoise runs n complex blocks of noise at the given amplitude
// through measureIF (the squelch state machine) without touching the
// audio path. Complex gaussian noise at amplitude a has channel power
// ≈ 2a² — amp 0.001 → ≈ −57 dBFS, amp 0.1 → ≈ −17 dBFS.
func feedNoise(c *Chain, amp float64, blocks int) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < blocks; i++ {
		blk := make([]complex128, 256)
		for j := range blk {
			blk[j] = complex(amp*r.NormFloat64(), amp*r.NormFloat64())
		}
		c.measureIF(blk)
	}
}

// TestSquelchAbsoluteDbfs: the threshold is absolute dBFS on the same
// meter the status bar shows — set −30, noise at −57 stays closed, a
// −17 dBFS level opens, dropping back closes (with 6 dB hysteresis),
// and ≥ 0 means off.
func TestSquelchAbsoluteDbfs(t *testing.T) {
	SetIQRate(2_048_000)
	c := NewChain(ModeNFM, nil, nil)
	c.SetSquelchDb(-30)

	feedNoise(c, 0.001, 100) // ≈ −57 dBFS
	if c.SquelchOpen() {
		t.Fatal("squelch open on noise 27 dB below the threshold")
	}

	feedNoise(c, 0.1, 100) // ≈ −17 dBFS
	if !c.SquelchOpen() {
		t.Fatal("squelch closed on a level 13 dB above the threshold")
	}

	// Hysteresis: −22 dBFS is above the close point (−36) but below the
	// open point (−30) — an open squelch must stay open there.
	feedNoise(c, 0.02, 50) // ≈ −27 dBFS…−22 region
	if !c.SquelchOpen() {
		t.Fatal("open squelch closed inside the hysteresis band")
	}

	feedNoise(c, 0.001, 100)
	if c.SquelchOpen() {
		t.Fatal("squelch stayed open after the level dropped below threshold−6")
	}

	// Threshold ≥ 0 = off: everything passes.
	c.SetSquelchDb(0)
	feedNoise(c, 0.001, 20)
	if !c.SquelchOpen() {
		t.Fatal("SQL 0 (off) must leave the squelch open on noise")
	}
}
