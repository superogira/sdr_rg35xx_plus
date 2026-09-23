package dsp

import (
	"math"
	"math/rand"
	"testing"
)

func TestFT8BitDiff(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const center = 1234.375
	msg := pack77("CQ", "HS0ZKO", "OK04")
	tones := encodeTones(msg)
	audio := synthFrame(tones, center, 1.0, 0, rng)

	// Extract LLRs at exact offset 0.
	llr := make([]float64, 174)
	mag := make([]float64, 8)
	k := 0
	for sym := 7; sym < 72; sym++ {
		if sym >= 36 && sym < 43 {
			continue
		}
		s := sym * FT8SymSamples
		for t2 := 0; t2 < 8; t2++ {
			mag[t2] = goertzelMag(audio[s:s+FT8SymSamples], center+(float64(t2)-3.5)*FT8ToneHz, float64(FT8AudioRate))
		}
		s2 := [8]float64{}
		for j := 0; j < 8; j++ {
			s2[j] = mag[FT8GrayMap[j]]
		}
		llr[3*k] = math.Max(math.Max(s2[4], s2[5]), math.Max(s2[6], s2[7])) - math.Max(math.Max(s2[0], s2[1]), math.Max(s2[2], s2[3]))
		llr[3*k+1] = math.Max(math.Max(s2[2], s2[3]), math.Max(s2[6], s2[7])) - math.Max(math.Max(s2[0], s2[1]), math.Max(s2[4], s2[5]))
		llr[3*k+2] = math.Max(math.Max(s2[1], s2[3]), math.Max(s2[5], s2[7])) - math.Max(math.Max(s2[0], s2[2]), math.Max(s2[4], s2[6]))
		k++
	}
	// Compare against expected hard bits from the tones directly.
	expTones := make([]int, 0, 58)
	for sym := 7; sym < 72; sym++ {
		if sym >= 36 && sym < 43 {
			continue
		}
		expTones = append(expTones, tones[sym])
	}
	bad := 0
	for ki, tone := range expTones {
		// hard decision from llr: find value v maximizing... check sign match
		v := 0
		if llr[3*ki] > 0 {
			v |= 4
		}
		if llr[3*ki+1] > 0 {
			v |= 2
		}
		if llr[3*ki+2] > 0 {
			v |= 1
		}
		if FT8GrayMap[v] != tone {
			bad++
			t.Logf("symbol k=%d (channel %d): tone=%d got value=%d llr=%.2f,%.2f,%.2f", ki, chSym(ki), tone, v, llr[3*ki], llr[3*ki+1], llr[3*ki+2])
		}
	}
	t.Logf("mismatched symbols: %d/58", bad)
}

func chSym(k int) int {
	if k < 29 {
		return 7 + k
	}
	return 43 + k - 29
}
