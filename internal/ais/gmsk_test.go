package ais

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// aivdmPayload extracts the raw payload bytes (LSB-first bit stream
// repacked into bytes) from an armored AIVDM payload string.
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
			out[i] |= bits[i*8+k] << k
		}
	}
	return out
}

// synthAISBurst modulates one AIS transmission (training + flag +
// stuffed payload+FCS + flag) into an FM-discriminator output with a
// matching power track, as the DSP front-end would deliver them.
func synthAISBurst(payload []byte, rate int, dev, dcOffset, fmNoise float64, rng *rand.Rand) (fm, power []float64) {
	var bits []byte
	for i := 0; i < 24; i++ {
		bits = append(bits, byte(i%2)) // 0101… training
	}
	flag := func() { bits = append(bits, 0, 1, 1, 1, 1, 1, 1, 0) }
	flag()
	fcs := crcX25(payload)
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
			if ones == 5 { // bit stuffing
				bits = append(bits, 0)
				ones = 0
			}
		}
	}
	flag()

	spb := float64(rate) / 9600.0
	lvl := 1.0
	cur := 0.0
	for bi, b := range bits {
		if b == 0 {
			lvl = -lvl // NRZI: 0 toggles
		}
		// Absolute symbol edges keep the average bit rate exact
		// (a naive per-bit loop rounds spb up per bit and drifts).
		start := int(float64(bi) * spb)
		end := int(float64(bi+1) * spb)
		for s := start; s < end; s++ {
			// One-pole smoothing stands in for the Gaussian filter.
			cur += 0.45 * (lvl - cur)
			fm = append(fm, dev*cur+dcOffset+rng.NormFloat64()*fmNoise)
			power = append(power, 1.0)
		}
	}
	return fm, power
}

func feedNoise(d *ChannelDemod, n int, rng *rand.Rand, fmNoise float64) {
	for i := 0; i < n; i += 512 {
		fm := make([]float64, 512)
		power := make([]float64, 512)
		for k := range fm {
			fm[k] = rng.NormFloat64() * fmNoise
			power[k] = 0.1 + rng.Float64()*0.05
		}
		d.Feed(fm, power)
	}
}

func TestCRCX25Vector(t *testing.T) {
	if got := crcX25([]byte("123456789")); got != 0x906E {
		t.Fatalf("crcX25 check value = %04X, want 906E", got)
	}
}

func TestGMSKRoundTrip(t *testing.T) {
	// Reference sentence payload: MMSI 366053209, type 1 position.
	payload := aivdmPayload("15M67FC000G?ufbE`FepT@3n00Sa")
	if len(payload) != 21 {
		t.Fatalf("payload len %d, want 21 (168 bits)", len(payload))
	}
	cases := []struct {
		name    string
		dc      float64 // LO error in the FM domain (ppm/DC)
		fmNoise float64
	}{
		{"clean", 0, 0},
		{"dc offset (ppm error)", 1500, 0},
		{"noise", 0, 600},
		{"dc + noise", 900, 500},
	}
	for _, c := range cases {
		rng := rand.New(rand.NewSource(42))
		var got []byte
		d := NewChannelDemod(64000, 0, "A", func(p []byte, ch int) {
			if got == nil {
				got = append([]byte{}, p...)
			}
		})
		feedNoise(d, 4096, rng, 900)
		fm, power := synthAISBurst(payload, 64000, 2400, c.dc, c.fmNoise, rng)
		d.Feed(fm, power)
		feedNoise(d, 4096, rng, 900)
		if got == nil {
			t.Fatalf("%s: no frame decoded", c.name)
		}
		if strings.Compare(string(got), string(payload)) != 0 {
			t.Fatalf("%s: payload mismatch\n got  % x\n want % x", c.name, got, payload)
		}
	}
}

func TestGMSKDecodeBitsIntegration(t *testing.T) {
	payload := aivdmPayload("15M67FC000G?ufbE`FepT@3n00Sa")
	rng := rand.New(rand.NewSource(7))
	s := NewStore()
	d := NewChannelDemod(64000, 1, "B", func(p []byte, ch int) {
		s.DecodeBits(p)
	})
	feedNoise(d, 2048, rng, 900)
	fm, power := synthAISBurst(payload, 64000, 2400, 0, 400, rng)
	d.Feed(fm, power)
	feedNoise(d, 4096, rng, 900)
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
	_ = math.Abs
}
