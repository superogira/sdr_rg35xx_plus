package ais

import (
	"math"
	"math/rand"
	"testing"
)

// aivdmPayload extracts the raw payload bytes from an armored AIVDM
// payload (bit stream repacked into bytes, LSB-first per octet).
func aivdmPayload(armored string) []byte {
	var bits []byte
	for k := 0; k < len(armored); k++ {
		c := armored[k]
		v := c - 48
		if v > 40 {
			v -= 8
		}
		for b := 5; b >= 0; b-- {
			bits = append(bits, (v>>uint(b))&1)
		}
	}
	out := make([]byte, len(bits)/8)
	for i := range out {
		for k := 0; k < 8; k++ {
			out[i] |= bits[i*8+k] << (7 - k)
		}
	}
	return out
}

// synthAISIQ modulates one AIS transmission (training + flags +
// stuffed payload+FCS) into channel-centred complex IQ at 48 ks/s with
// GMSK phase smoothing, a carrier phase offset, a frequency offset and
// complex noise.
func synthAISIQ(payload []byte, carrierPhase, freqOffset, noise float64, rng *rand.Rand) []complex128 {
	bits := frameAirBits(payload, 24)

	spb := 48000.0 / 9600.0
	lvl := 1.0 // NRZI level (0 toggles)
	cur := 0.0
	ph := carrierPhase
	var out []complex128
	for bi, b := range bits {
		if b == 0 {
			lvl = -lvl
		}
		start := int(float64(bi) * spb)
		end := int(float64(bi+1) * spb)
		for s := start; s < end; s++ {
			cur += 0.45 * (lvl - cur)
			// MSK h=0.5: ±R/4 = ±2400 Hz; a fixed offset models ppm.
			ph += 2 * math.Pi * (2400*cur + freqOffset) / 48000.0
			z := complex(math.Cos(ph), math.Sin(ph))
			if noise > 0 {
				z += complex(rng.NormFloat64()*noise, rng.NormFloat64()*noise)
			}
			out = append(out, z)
		}
	}
	return out
}

func TestGMSKRoundTrip(t *testing.T) {
	payload := aivdmPayload("15M67FC000G?ufbE`FepT@3n00Sa")
	if len(payload) != 21 {
		t.Fatalf("payload len %d, want 21", len(payload))
	}
	cases := []struct {
		name  string
		phase float64
		foff  float64
		noise float64
	}{
		{"clean", 0, 0, 0},
		{"carrier phase", 1.1, 0, 0},
		// Offset tolerance is ±1.2 kHz (the 4th-power discriminator
		// wraps at ±π/4 per symbol); larger residual ppm is removed by
		// the app's Freq Correction setting, which the server applies
		// to the LO before the IQ reaches this demod.
		{"freq offset 700", 0, 700, 0},
		{"freq offset 1100", 0, 1100, 0},
		{"noise", 0, 0, 0.2},
		{"phase+offset+noise", 0.7, 500, 0.15},
	}
	for _, c := range cases {
		rng := rand.New(rand.NewSource(42))
		var got []byte
		d := NewChannelDemod(48000, 0, "A", func(p []byte, ch int, levelDb float64) {
			if got == nil {
				got = append([]byte{}, p...)
			}
		})
		iq := synthAISIQ(payload, c.phase, c.foff, c.noise, rng)
		// Leading noise-only stretch so the classifier settles.
		for i := 0; i < 4800; i++ {
			z := complex(rng.NormFloat64()*0.25, rng.NormFloat64()*0.25)
			d.Feed([]complex128{z})
		}
		d.Feed(iq)
		// Trailing noise flushes the matched filter's group delay —
		// in live reception the stream continues, so the tail always
		// emerges with the next block.
		for i := 0; i < 2400; i++ {
			z := complex(rng.NormFloat64()*0.25, rng.NormFloat64()*0.25)
			if c.noise == 0 {
				z = 0
			}
			d.Feed([]complex128{z})
		}
		if got == nil {
			t.Errorf("%s: no frame decoded", c.name)
			continue
		}
		if string(got) != string(payload) {
			t.Errorf("%s: payload mismatch\n got  % x\n want % x", c.name, got, payload)
		}
	}
}

func TestGMSKDecodeBitsIntegration(t *testing.T) {
	payload := aivdmPayload("15M67FC000G?ufbE`FepT@3n00Sa")
	rng := rand.New(rand.NewSource(7))
	s := NewStore()
	d := NewChannelDemod(48000, 1, "B", func(p []byte, ch int, levelDb float64) {
		s.DecodeBits(p)
	})
	iq := synthAISIQ(payload, 0.3, 700, 0.18, rng)
	d.Feed(iq)
	for i := 0; i < 2400; i++ {
		d.Feed([]complex128{complex(rng.NormFloat64()*0.18, rng.NormFloat64()*0.18)})
	}
	sh := s.Ships()
	if len(sh) != 1 {
		t.Fatalf("ships = %d, want 1", len(sh))
	}
	if sh[0].MMSI != "366053209" {
		t.Fatalf("MMSI = %s, want 366053209", sh[0].MMSI)
	}
	if !sh[0].HasPos {
		t.Fatal("no position decoded")
	}
}

// frameAirBits builds the on-air bit sequence (training + flag +
// stuffed payload+FCS + flag) for a payload, in the AIS air order:
// octets MSB-first, FCS = complement of the post-data X.25 state with
// its bits LSB-first (the receiver's reflected loop then sees the
// 0xF0B8 residue).
func frameAirBits(payload []byte, training int) []byte {
	var bits []byte
	for i := 0; i < training; i++ {
		bits = append(bits, byte(i%2))
	}
	flag := func() { bits = append(bits, 0, 1, 1, 1, 1, 1, 1, 0) }
	flag()
	// Air order: HDLC LSB-first per octet.
	var air []byte
	for _, by := range payload {
		for k := 0; k < 8; k++ {
			air = append(air, (by>>uint(k))&1)
		}
	}
	crc := uint32(0xFFFF)
	for _, b := range air {
		if (uint32(b)^crc)&1 != 0 {
			crc = (crc >> 1) ^ 0x8408
		} else {
			crc >>= 1
		}
	}
	inv := uint16(^crc)
	for i := 0; i < 16; i++ {
		air = append(air, byte((inv>>uint(i))&1))
	}
	ones := 0
	for _, b := range air {
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
	flag()
	return bits
}
