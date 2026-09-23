package dsp

import (
	"math/rand"
	"testing"
)

// TestOSDRecoversFlips builds a known codeword, builds LLRs with a
// controlled number of bit errors biased onto the weakest reliabilities,
// and checks OSD recovers the original codeword exactly.
func TestOSDRecoversFlips(t *testing.T) {
	rng := rand.New(rand.NewSource(77))
	payload := pack77("K1ABC", "W9XYZ", "EN37")
	var pbytes [10]byte
	ft8PackBits(payload, 77, pbytes[:])
	var a91 [12]byte
	ft8AddCRC(pbytes[:], a91[:])
	var cw [22]byte
	ft8Encode174(a91[:], cw[:])
	bits := make([]int, 174)
	mask := byte(0x80)
	bi := 0
	for i := 0; i < 174; i++ {
		if cw[bi]&mask != 0 {
			bits[i] = 1
		}
		mask >>= 1
		if mask == 0 {
			mask = 0x80
			bi++
		}
	}
	for _, nErr := range []int{2, 3, 4} {
		for trial := 0; trial < 20; trial++ {
			// reliabilities: strong for correct bits, weak where we flip
			llr := make([]float64, 174)
			pos := rng.Perm(174)[:nErr]
			flipped := map[int]bool{}
			for _, p := range pos {
				flipped[p] = true
			}
			for i := 0; i < 174; i++ {
				v := 3.0
				if flipped[i] {
					v = 0.2
				}
				b := bits[i]
				if b == 1 {
					llr[i] = v
				} else {
					llr[i] = -v
				}
			}
			got := ft8OSD(llr, ft8OSDOrder)
			if got == nil {
				t.Fatalf("nErr=%d trial %d: OSD nil", nErr, trial)
			}
			for i := 0; i < 174; i++ {
				if got[i] != bits[i] {
					t.Fatalf("nErr=%d trial %d: bit %d mismatch (got %d want %d)", nErr, trial, i, got[i], bits[i])
				}
			}
		}
	}
}
