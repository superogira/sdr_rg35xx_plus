package dsp

import (
	"math"
	"math/rand"
	"testing"

	"sdr35/internal/ais"
)

// synthChannelIQ builds complex IQ at 256 ks/s for one AIS channel at
// frequency f: NRZI-GMSK-ish FM with dev 2400 Hz, upsampled x4 from
// the 64k symbol-rate domain.
func synthChannelIQ(bits []byte, f float64, up int, rng *rand.Rand) []complex128 {
	spb := 64000.0 / 9600.0
	// level per 64k-sample
	lvl := 1.0
	var y []float64 // fm value at 64k
	cur := 0.0
	for bi, b := range bits {
		if b == 0 {
			lvl = -lvl
		}
		start := int(float64(bi) * spb)
		end := int(float64(bi+1) * spb)
		for s := start; s < end; s++ {
			cur += 0.45 * (lvl - cur)
			y = append(y, 2400*cur+rng.NormFloat64()*300)
		}
	}
	// integrate phase at 64k then upsample (hold) and mix to f.
	out := make([]complex128, len(y)*up)
	ph := 0.0
	for i, v := range y {
		ph += 2 * math.Pi * v / 64000.0
		c := complex(math.Cos(ph), math.Sin(ph))
		for u := 0; u < up; u++ {
			t := float64(i*up+u) / float64(64000*up)
			out[i*up+u] = c * complex(math.Cos(2*math.Pi*f*t), math.Sin(2*math.Pi*f*t))
		}
	}
	return out
}

func aisBits(payload []byte, crc func([]byte) uint16) []byte {
	var bits []byte
	for i := 0; i < 24; i++ {
		bits = append(bits, byte(i%2))
	}
	flag := func() { bits = append(bits, 0, 1, 1, 1, 1, 1, 1, 0) }
	flag()
	fcs := crc(payload)
	frame := append(append([]byte{}, payload...), byte(fcs), byte(fcs>>8))
	ones := 0
	for _, by := range frame {
		for k := 0; k < 8; k++ {
			b := (by >> k) & 1
			bits = append(bits, b)
			if b == 1 {
				ones++
			} else {
				ones = 0
			}
			if ones == 5 {
				bits = append(bits, 0)
				ones = 0
			}
		}
	}
	flag()
	return bits
}

func TestFeedAISBothChannels(t *testing.T) {
	if IF2Rate != 256000 {
		t.Skipf("IF2Rate=%d", IF2Rate)
	}
	rng := rand.New(rand.NewSource(3))
	// Two distinct payloads (A = type-1-ish filler, B different).
	payA := make([]byte, 21)
	payB := make([]byte, 21)
	for i := range payA {
		payA[i] = byte(i * 11)
		payB[i] = byte(i*7 + 3)
	}
	iqA := synthChannelIQ(aisBits(payA, x25), -25000, 4, rng)
	iqB := synthChannelIQ(aisBits(payB, x25), 25000, 4, rng)
	n := len(iqA)
	if len(iqB) > n {
		n = len(iqB)
	}
	iq := make([]complex128, n)
	for i := 0; i < n; i++ {
		a := complex(0, 0)
		if i < len(iqA) {
			a += iqA[i]
		}
		if i < len(iqB) {
			a += iqB[i]
		}
		// ambient noise
		a += complex(rng.NormFloat64()*0.05, rng.NormFloat64()*0.05)
		iq[i] = a
	}
	c := NewChain(ModeNFM, nil, nil)
	var gotA, gotB [][]byte
	// The real over-the-air demodulators.
	c.SetAISDemods(
		ais.NewChannelDemod(64000, 0, "A", func(p []byte, ch int) { gotA = append(gotA, p) }),
		ais.NewChannelDemod(64000, 1, "B", func(p []byte, ch int) { gotB = append(gotB, p) }),
	)
	// Padding is ambient noise (zero IQ would pin the power floor at
	// zero and the burst end condition could never fire).
	pad := make([]complex128, 2560)
	for i := range pad {
		pad[i] = complex(rng.NormFloat64()*0.05, rng.NormFloat64()*0.05)
	}
	c.feedAIS(pad)
	for len(iq) > 0 {
		chunk := iq
		if len(chunk) > 4096 {
			chunk = iq[:4096]
		}
		c.feedAIS(chunk)
		iq = iq[len(chunk):]
	}
	c.feedAIS(pad)
	if len(gotA) == 0 || len(gotB) == 0 {
		t.Fatalf("A frames=%d B frames=%d", len(gotA), len(gotB))
	}
	if string(gotA[0]) != string(payA) {
		t.Errorf("channel A payload mismatch")
	}
	if string(gotB[0]) != string(payB) {
		t.Errorf("channel B payload mismatch")
	}
}

func x25(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, ch := range b {
		crc ^= uint16(ch)
		for k := 0; k < 8; k++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFF
}
