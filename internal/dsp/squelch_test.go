package dsp

import (
	"math"
	"math/rand"
	"testing"
)

// feedNoise runs n complex blocks of noise at the given amplitude
// through measureIF (the squelch state machine) without touching the
// audio path.
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

// TestSquelchStuckOpenRecovery: regression for the "36 dB and the hiss
// never stops" report. The noise floor freezes while the squelch is
// open, so a permanent level rise that happened while open (monitoring
// with SQL off, then setting a threshold) used to leave the floor stale
// far below the real noise — the close condition could never fire
// again. Setting a squelch level must re-learn the floor and close.
func TestSquelchStuckOpenRecovery(t *testing.T) {
	SetIQRate(2_048_000)
	c := NewChain(ModeNFM, nil, nil)
	c.SetSquelchDb(36)

	// 1. Quiet noise; squelch closes and the floor settles on it.
	feedNoise(c, 0.001, 300)
	if c.SquelchOpen() {
		t.Fatal("squelch open on pure noise at start")
	}
	quietFloor := c.sqlFloor

	// 2. Noise jumps 40 dB while "open" — e.g. gain raised during SQL
	// OFF monitoring. The squelch opens and the floor freezes low.
	feedNoise(c, 0.1, 300)
	if !c.SquelchOpen() {
		t.Fatal("expected squelch to open on a +40 dB level jump")
	}
	if c.sqlFloor > quietFloor+10 {
		t.Fatalf("floor should be frozen near the quiet level, got %.0f vs %.0f", c.sqlFloor, quietFloor)
	}

	// 3. Old behaviour: stuck open forever — noise at +40 dB over the
	// stale floor beats the close point (floor+30).
	feedNoise(c, 0.1, 50)
	if !c.SquelchOpen() {
		t.Fatal("test premise broken: squelch closed without a reset")
	}

	// 4. User sets the squelch level (SetSquelchDb on the chain +
	// ResetSqlFloor, as radio.SetSquelchDb now does): the floor
	// re-learns the CURRENT noise and the squelch closes.
	c.SetSquelchDb(36)
	c.ResetSqlFloor()
	feedNoise(c, 0.1, 50)
	if c.SquelchOpen() {
		t.Fatal("squelch still open after re-learning the floor — stuck-open bug is back")
	}

	// 5. A real signal 40 dB over the re-learned floor must still open
	// it at a 36 dB threshold (a +20 dB one legitimately stays closed).
	feedNoise(c, 0.1*100, 50)
	if !c.SquelchOpen() {
		t.Fatal("squelch fails to open on a genuine +40 dB signal")
	}
	_ = math.Abs
}
