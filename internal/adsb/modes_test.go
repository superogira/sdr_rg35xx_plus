package adsb

import (
	"math"
	"testing"
)

// ppmModulate shapes a full Mode S transmission: preamble pulses then
// PPM data, as IQ samples at the given rate with noise.
func ppmModulate(msg []byte, rate float64, amp, noise float64, seed uint64) []byte {
	bits := bytesToBits(msg)
	total := 8.0 + float64(len(bits))
	n := int(total*rate/1e6 + 0.999) // ceil: truncating dropped the final CRC bits
	samples := make([]complex128, n)
	// Textbook pulses: every sample whose CENTRE falls inside the 0.5 µs
	// pulse is set to full amplitude (the decision windows also use
	// sample-centre semantics, so modulator and demodulator agree).
	pulse := func(us float64) {
		ts := us * rate / 1e6
		te := ts + 0.5*rate/1e6
		for i := 0; i < n; i++ {
			c := float64(i) + 0.5
			if c >= ts && c < te {
				samples[i] = complex(1, 0)
			}
		}
	}
	// Mode S preamble: 0.5 us pulses at 0, 1.0, 3.5 and 4.5 us.
	pulse(0)
	pulse(1)
	pulse(3.5)
	pulse(4.5)
	for k, b := range bits {
		us := 8.0 + float64(k)
		if b == 1 {
			pulse(us)
		} else {
			pulse(us + 0.5)
		}
	}
	out := make([]byte, 0, n*2)
	rnd := seed
	for _, z := range samples {
		rnd = rnd*6364136223846793005 + 1442695040888963407
		nr := (float64(int64(rnd>>33)%2000)/1000.0 - 1) * noise
		rnd = rnd*6364136223846793005 + 1442695040888963407
		ni := (float64(int64(rnd>>33)%2000)/1000.0 - 1) * noise
		re := 127.5 * (real(z)*amp + nr)
		im := 127.5 * (imag(z)*amp + ni)
		out = append(out, byte(re+127.5), byte(im+127.5))
	}
	return out
}

func TestCRC24ZeroForValid(t *testing.T) {
	// Any message with its CRC appended must check to zero.
	data := appendCRC24([]byte{0x8D, 0x40, 0x6B, 0x90, 0x20, 0x15, 0xA6, 0x78, 0xD4, 0xD2, 0x20})
	if crc24(bytesToBits(data)) != 0 {
		t.Fatal("CRC round-trip failed")
	}
	// Flip one bit → non-zero.
	data[3] ^= 0x40
	if crc24(bytesToBits(data)) == 0 {
		t.Fatal("CRC did not catch a bit error")
	}
}

func TestModeSDemodDecodes(t *testing.T) {
	// The app runs the RTL at exactly 2.4 MSPS for ADS-B (the standard
	// rate dump1090/readsb require); the demod also accepts integer
	// multiples of 2.4M at which the 2.4M grid lands on source samples.
	for _, rate := range []float64{2_400_000, 3_200_000, 4_800_000} {
		s := NewStore()
		icao := [3]byte{0x88, 0x41, 0xF2}
		// Position pair + callsign + velocity, spaced with idle gaps.
		var air []byte
		// 2 ms of noise-only IQ around the 127.5 baseline — plain
		// zero bytes would be a full-scale DC carrier.
		idle := ppmIdle(int(0.002*rate), 0.12, 7)
		for _, m := range [][]byte{
			appendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, true)),
			appendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, false)),
			appendCRC24(encodeCallsignFrame(icao, "THA341")),
			appendCRC24(encodeVelocityFrame(icao, 450, 123)),
		} {
			air = append(air, idle...)
			air = append(air, ppmModulate(m, rate, 0.7, 0.05, 42)...)
		}
		// Trailing idle: the demod scans positions with 125 µs of
		// subsequent history, so the final message needs samples after
		// it before it can be read.
		air = append(air, idle...)
		d := NewModeSDemod(rate, s)
		for i := 0; i < len(air); i += 65536 {
			end := i + 65536
			if end > len(air) {
				end = len(air)
			}
			d.FeedIQ(air[i:end])
		}
		ps := s.Planes()
		if len(ps) != 1 || !ps[0].HasPos {
			t.Fatalf("rate %.3fM: no decode (%+v)", rate/1e6, ps)
		}
		p := ps[0]
		if math.Abs(p.Lat-13.70) > 0.001 || math.Abs(p.Lon-100.60) > 0.001 {
			t.Fatalf("rate %.3fM: position %.4f %.4f", rate/1e6, p.Lat, p.Lon)
		}
		if p.Callsign != "THA341" || p.SpeedKt < 445 || p.SpeedKt > 455 {
			t.Fatalf("rate %.3fM: cs=%q spd=%d", rate/1e6, p.Callsign, p.SpeedKt)
		}
		t.Logf("rate %.2fM: decoded %s pos %.3f,%.3f alt %d", rate/1e6, p.Callsign, p.Lat, p.Lon, p.AltFt)
	}
}

// ppmIdle produces n noise-only IQ samples at the u8 baseline.
func ppmIdle(n int, noise float64, seed uint64) []byte {
	out := make([]byte, 0, n*2)
	rnd := seed
	for i := 0; i < n; i++ {
		rnd = rnd*6364136223846793005 + 1442695040888963407
		nr := (float64(int64(rnd>>33)%2000)/1000.0 - 1) * noise
		rnd = rnd*6364136223846793005 + 1442695040888963407
		ni := (float64(int64(rnd>>33)%2000)/1000.0 - 1) * noise
		out = append(out, byte(127.5+nr*127.5), byte(127.5+ni*127.5))
	}
	return out
}
