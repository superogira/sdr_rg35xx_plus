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
	// Generate at the full IF2-input rate (256k = 64000*up) with
	// per-sample GMSK-ish smoothing — a hold-upsampled 64k phase
	// staircase would smear the coherent demodulator.
	rate := float64(64000 * up)
	spb := rate / 9600.0
	lvl := 1.0
	cur := 0.0
	ph := 0.0
	var out []complex128
	for bi, b := range bits {
		if b == 0 {
			lvl = -lvl
		}
		start := int(float64(bi) * spb)
		end := int(float64(bi+1) * spb)
		for s := start; s < end; s++ {
			cur += 0.45 * (lvl - cur) / float64(up)
			ph += 2 * math.Pi * (2400*cur + f) / rate
			out = append(out, complex(math.Cos(ph), math.Sin(ph)))
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
	// Two distinct VALID payloads (the type field must be a real
	// message type or cannotBeValid aborts the frame): the reference
	// type-1 payload and a mutation of it.
	var abits []byte
	armor := "15M67FC000G?ufbE`FepT@3n00Sa"
	for k := 0; k < len(armor); k++ {
		v := armor[k] - 48
		if v > 40 {
			v -= 8
		}
		for b := 5; b >= 0; b-- {
			abits = append(abits, (v>>uint(b))&1)
		}
	}
	payA := make([]byte, 21)
	for i := 0; i+7 < len(abits); i += 8 {
		by := byte(0)
		for k := 0; k < 8; k++ {
			by |= abits[i+k] << k
		}
		payA[i/8] = by
	}
	payB := append([]byte{}, payA...)
	payB[20] ^= 0x55
	// Three bursts per channel with gaps — live AIS traffic is
	// continuous, and a mid-stream AFC slew must not cost more than
	// the burst it lands on.
	var iq []complex128
	gap := func() {
		for i := 0; i < 5120; i++ {
			iq = append(iq, complex(rng.NormFloat64()*0.05, rng.NormFloat64()*0.05))
		}
	}
	for rep := 0; rep < 3; rep++ {
		iqA := synthChannelIQ(aisBits(payA, x25), -25000, 4, rng)
		iqB := synthChannelIQ(aisBits(payB, x25), 25000, 4, rng)
		n := len(iqA)
		if len(iqB) > n {
			n = len(iqB)
		}
		for i := 0; i < n; i++ {
			a := complex(0, 0)
			if i < len(iqA) {
				a += iqA[i]
			}
			if i < len(iqB) {
				a += iqB[i]
			}
			a += complex(rng.NormFloat64()*0.05, rng.NormFloat64()*0.05)
			iq = append(iq, a)
		}
		gap()
	}
	c := NewChain(ModeNFM, nil, nil)
	var gotA, gotB [][]byte
	// The real over-the-air demodulators (feedAIS resamples to 48k).
	c.SetAISDemods(
		ais.NewChannelDemod(48000, 0, "A", func(p []byte, ch int) { gotA = append(gotA, p) }),
		ais.NewChannelDemod(48000, 1, "B", func(p []byte, ch int) { gotB = append(gotB, p) }),
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
	for _, p := range gotA {
		if string(p) != string(payA) {
			t.Fatalf("channel A payload mismatch")
		}
	}
	for _, p := range gotB {
		if string(p) != string(payB) {
			t.Fatalf("channel B payload mismatch")
		}
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
