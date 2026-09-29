package adsb

import (
	"math"
	"testing"
)

func TestDemodMultiDbg(t *testing.T) {
	modesDebug = func(f string, a ...any) { t.Logf(f, a...) }
	defer func() { modesDebug = nil }()
	s := NewStore()
	icao := [3]byte{0x88, 0x41, 0xF2}
	rate := 2_048_000.0
	var air []byte
	idle := ppmIdle(int(0.002*rate), 0.12, 7)
	for _, m := range [][]byte{
		appendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, true)),
		appendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, false)),
	} {
		air = append(air, idle...)
		air = append(air, ppmModulate(m, rate, 0.6, 0.12, 42)...)
	}
	air = append(air, idle...)
	d := NewModeSDemod(rate, s)
	for i := 0; i < len(air); i += 16384 {
		end := i + 16384
		if end > len(air) {
			end = len(air)
		}
		d.FeedIQ(air[i:end])
	}
	for k := 0; k < 4; k++ {
		base := float64(4096) + (8.0+float64(k))*rate/1e6
		var mags []float64
		for i := int(base) - 1; i <= int(base+2.5*rate/1e6)+1; i++ {
			re := float64(air[i*2]) - 127.5
			im := float64(air[i*2+1]) - 127.5
			mags = append(mags, math.Sqrt(re*re+im*im))
		}
		t.Logf("bit%d raw window: %.0f", k, mags)
	}

	t.Logf("planes=%d", len(s.Planes()))
	var mags []float64
	for i := 0; i < 16; i++ {
		re := float64(air[(4096+i)*2]) - 127.5
		im := float64(air[(4096+i)*2+1]) - 127.5
		mags = append(mags, math.Sqrt(re*re+im*im))
	}
	t.Logf("mags@4096: %.0f", mags)
	var m0 []float64
	for i := 0; i < 16; i++ {
		re := float64(air[i*2]) - 127.5
		im := float64(air[i*2+1]) - 127.5
		m0 = append(m0, math.Sqrt(re*re+im*im))
	}
	t.Logf("mags@0 (idle): %.0f", m0)
}
