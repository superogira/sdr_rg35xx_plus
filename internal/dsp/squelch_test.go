package dsp

import (
	"math"
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

// feedCW runs n blocks of a constant-envelope carrier whose meter
// reading is exactly 20·log10(a) dBFS — deterministic levels for
// threshold-edge tests.
func feedCW(c *Chain, dbfs float64, blocks int) {
	z := complex(math.Pow(10, dbfs/20), 0)
	for i := 0; i < blocks; i++ {
		blk := make([]complex128, 256)
		for j := range blk {
			blk[j] = z
		}
		c.measureIF(blk)
	}
}

// TestSquelchAbsoluteDbfs: the threshold is absolute dBFS on the same
// meter the status bar shows — set −30, noise at −57 stays closed, a
// −17 dBFS level opens, dropping back closes (with 2 dB hysteresis),
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

	// Hysteresis: −31 dBFS is above the close point (−32) but below the
	// open point (−30) — an open squelch must stay open there.
	feedCW(c, -31, 50)
	if !c.SquelchOpen() {
		t.Fatal("open squelch closed inside the hysteresis band")
	}

	feedNoise(c, 0.001, 100)
	if c.SquelchOpen() {
		t.Fatal("squelch stayed open after the level dropped below threshold−2")
	}

	// Threshold ≥ 0 = off: everything passes.
	c.SetSquelchDb(0)
	feedNoise(c, 0.001, 20)
	if !c.SquelchOpen() {
		t.Fatal("SQL 0 (off) must leave the squelch open on noise")
	}
}

// A signal that sits BETWEEN the threshold and the close level must not
// keep the squelch latched open. Field report: SQL −31 dBFS with the
// meter parked at −33 still played audio — the old −6 dB hysteresis put
// the close level at −37, so anything above −37 that had once peaked
// over −31 stayed open forever, and only raising SQL to −27 silenced
// it. The zone is now 2 dB: a −35 carrier under a −31 threshold must
// close (and stay closed).
func TestSquelchHysteresisClosesNearThreshold(t *testing.T) {
	SetIQRate(2_048_000)
	c := NewChain(ModeNFM, nil, nil)
	c.SetSquelchDb(-31)

	feedCW(c, -57, 100)
	if c.SquelchOpen() {
		t.Fatal("squelch open on a weak carrier far below the threshold")
	}

	feedCW(c, -29, 100)
	if !c.SquelchOpen() {
		t.Fatal("squelch closed on a carrier above the threshold")
	}

	// −35: 4 dB under the threshold — outside the 2 dB zone, must
	// close. (The old −6 zone latched this open; close needed < −37.)
	feedCW(c, -35, 100)
	t.Logf("meter settled at %.1f dBFS, sqlOpen=%v", c.PowerDb(), c.SquelchOpen())
	if c.SquelchOpen() {
		t.Fatalf("squelch latched open: meter %.1f dBFS with SQL −31 (close level −33)",
			c.PowerDb())
	}
}

// The shrunken hysteresis still guards against flutter: a level inside
// the 2 dB zone holds an open squelch open, so a signal fading around
// the threshold does not gate the audio every block.
func TestSquelchHysteresisStillAntiflutter(t *testing.T) {
	SetIQRate(2_048_000)
	c := NewChain(ModeNFM, nil, nil)
	c.SetSquelchDb(-40)

	feedCW(c, -39.5, 100) // just above: opens
	if !c.SquelchOpen() {
		t.Fatal("squelch never opened just above the threshold")
	}
	for i := 0; i < 6; i++ {
		feedCW(c, -41.5, 20) // inside the zone (close < −42): hold open
		feedCW(c, -39.5, 20)
	}
	if !c.SquelchOpen() {
		t.Fatal("squelch flapped closed inside the hysteresis zone")
	}
}
