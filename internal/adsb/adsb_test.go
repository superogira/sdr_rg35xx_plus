// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package adsb

import (
	"math"
	"strings"
	"testing"
	"time"
)

// ---- test encoder (inverse of the decoder) ----

func cprEncodeLat(lat float64, odd bool) int {
	dlat := 360.0 / 60.0
	if odd {
		dlat = 360.0 / 59.0
	}
	// Guard: lat may sit exactly on a zone boundary; Mod handles sign.
	frac := math.Mod(math.Mod(lat, dlat)+dlat, dlat) / dlat
	return int(math.Floor(frac*131072+0.5)) & 0x1FFFF
}

func cprEncodeLon(lat, lon float64, odd bool) int {
	i := 0
	if odd {
		i = 1
	}
	ni := cprNL(lat) - i
	if ni < 1 {
		ni = 1
	}
	dlon := 360.0 / float64(ni)
	frac := math.Mod(math.Mod(lon, dlon)+dlon, dlon) / dlon
	return int(math.Floor(frac*131072+0.5)) & 0x1FFFF
}

func encodeAlt25(altFt int) int {
	v := (altFt + 1000) / 25
	return (v & 0xF) | ((v & 0x7F0) << 1) | 0x10
}

// encodePosFrame builds a 14-byte DF17 airborne-position message.
func encodePosFrame(icao [3]byte, lat, lon float64, altFt int, odd bool) []byte {
	tc := 11
	latC := cprEncodeLat(lat, odd)
	lonC := cprEncodeLon(lat, lon, odd)
	ac := encodeAlt25(altFt)
	f := 0
	if odd {
		f = 1
	}
	// 11 data bytes; AppendCRC24 grows this to the 14-byte (112-bit)
	// Mode S long frame.
	msg := make([]byte, 11)
	msg[0] = 0x8D // DF17 CA=5
	msg[1], msg[2], msg[3] = icao[0], icao[1], icao[2]
	me := msg[4:11]
	me[0] = byte(tc<<3 | 0<<1 | 0)
	me[1] = byte(ac >> 4)
	me[2] = byte((ac&0xF)<<4 | f<<2 | (latC>>15)&3)
	me[3] = byte(latC >> 7)
	me[4] = byte((latC&0x7F)<<1 | (lonC>>16)&1)
	me[5] = byte(lonC >> 8)
	me[6] = byte(lonC)
	return msg
}

func encodeCallsignFrame(icao [3]byte, call string) []byte {
	msg := make([]byte, 11)
	msg[0] = 0x8D
	msg[1], msg[2], msg[3] = icao[0], icao[1], icao[2]
	me := msg[4:11]
	me[0] = byte(4 << 3) // TC 4
	call = call + "        "
	for i := 0; i < 8; i++ {
		c := call[i]
		var v int
		switch {
		case c >= 'A' && c <= 'Z':
			v = int(c-'A') + 1
		case c >= '0' && c <= '9':
			v = int(c-'0') + 48
		default:
			v = 32
		}
		_ = i
		callsign6[i] = v
	}
	pack6(me, callsign6)
	return msg
}

var callsign6 [8]int

func pack6(me []byte, v [8]int) {
	b := func(a, b int) byte {
		return byte((v[a] >> (6 - b)) & 0x3F)
	}
	me[1] = byte(v[0]<<2 | v[1]>>4)
	me[2] = byte(v[1]<<4 | v[2]>>2)
	me[3] = byte(v[2]<<6 | v[3])
	me[4] = byte(v[4]<<2 | v[5]>>4)
	me[5] = byte(v[5]<<4 | v[6]>>2)
	me[6] = byte(v[6]<<6 | v[7])
	_ = b
}

func encodeVelocityFrame(icao [3]byte, speedKt, trackDeg int) []byte {
	rad := float64(trackDeg) * math.Pi / 180
	ew := int(math.Round(float64(speedKt) * math.Sin(rad)))
	ns := int(math.Round(float64(speedKt) * math.Cos(rad)))
	msg := make([]byte, 11)
	msg[0] = 0x8D
	msg[1], msg[2], msg[3] = icao[0], icao[1], icao[2]
	me := msg[4:11]
	me[0] = byte(19<<3 | 1)
	if ew < 0 {
		me[1] |= 0x04
		ew = -ew
	}
	me[1] |= byte((ew >> 8) & 3)
	me[2] = byte(ew & 0xFF)
	if ns < 0 {
		me[3] |= 0x80
		ns = -ns
	}
	me[3] |= byte((ns >> 3) & 0x7F)
	me[4] |= byte((ns & 7) << 5)
	return msg
}

// ---- tests ----

func TestPositionRoundTrip(t *testing.T) {
	s := NewStore()
	icao := [3]byte{0x40, 0x6B, 0x90}
	// Odd first, then even — the newest frame (even) selects the grid.
	s.Decode(AppendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, true)))
	s.Decode(AppendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, false)))
	ps := s.Planes()
	if len(ps) != 1 || !ps[0].HasPos {
		t.Fatalf("no position: %+v", ps)
	}
	p := ps[0]
	if math.Abs(p.Lat-13.70) > 0.001 || math.Abs(p.Lon-100.60) > 0.001 {
		t.Fatalf("position off: %.5f %.5f", p.Lat, p.Lon)
	}
	if p.AltFt != 35000 {
		t.Fatalf("altitude %d, want 35000", p.AltFt)
	}
	if p.ICAO != "406B90" {
		t.Fatalf("icao %s", p.ICAO)
	}
}

func TestCallsignAndVelocityRoundTrip(t *testing.T) {
	s := NewStore()
	icao := [3]byte{0x88, 0x41, 0xF2}
	s.Decode(AppendCRC24(encodeCallsignFrame(icao, "THA341")))
	s.Decode(AppendCRC24(encodeVelocityFrame(icao, 450, 123)))
	ps := s.Planes()
	if len(ps) != 1 {
		t.Fatalf("planes %d", len(ps))
	}
	p := ps[0]
	if strings.TrimSpace(p.Callsign) != "THA341" {
		t.Fatalf("callsign %q", p.Callsign)
	}
	if p.SpeedKt < 445 || p.SpeedKt > 455 {
		t.Fatalf("speed %d, want ~450", p.SpeedKt)
	}
	if p.TrackDeg < 120 || p.TrackDeg > 126 {
		t.Fatalf("track %d, want ~123", p.TrackDeg)
	}
}

func TestBeastFraming(t *testing.T) {
	var got [][]byte
	d := NewBeastDecoder(func(msg []byte, mlat uint64, sig int) {
		cp := make([]byte, len(msg))
		copy(cp, msg)
		got = append(got, cp)
	})
	payload := []byte{0x8D, 0x40, 0x1a, 0x00, 0x1a, 0x1a, 0x33, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66}
	frame := []byte{0x1a, 0x33, 0, 1, 2, 3, 4, 5, 0x77}
	escaped := append([]byte{}, frame...)
	// escape every 0x1a in the payload
	for i := len(frame); i < len(escaped); i++ {
	}
	// build escaped payload manually
	escPayload := []byte{}
	for _, b := range payload {
		if b == 0x1a {
			escPayload = append(escPayload, 0x1a, 0x1a)
		} else {
			escPayload = append(escPayload, b)
		}
	}
	stream := append(append([]byte{}, escaped...), escPayload...)
	// second simple frame
	stream = append(stream, 0x1a, 0x33, 9, 8, 7, 6, 5, 4, 0x55, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14)

	// feed in awkward chunks
	for i := 0; i < len(stream); i += 5 {
		end := i + 5
		if end > len(stream) {
			end = len(stream)
		}
		d.Feed(stream[i:end])
	}
	if len(got) != 2 {
		t.Fatalf("frames %d, want 2", len(got))
	}
	if string(got[0]) != string(payload) {
		t.Fatalf("payload 0 mismatch: %x", got[0])
	}
	for i, b := range got[0] {
		if b == 0x1a && payload[i] != 0x1a {
			t.Fatal("unescaping broken")
		}
	}
	_ = time.Now
}

func TestBeastResyncMidFrame(t *testing.T) {
	// Truncated frame followed by a fresh sync byte: the framer must
	// resync and still deliver the good frame.
	var got int
	d := NewBeastDecoder(func(msg []byte, mlat uint64, sig int) { got++ })
	bad := []byte{0x1a, 0x33, 1, 2, 3} // cut short
	good := append([]byte{0x1a, 0x33, 1, 2, 3, 4, 5, 6, 7}, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14)
	d.Feed(append(bad, good...))
	if got != 1 {
		t.Fatalf("frames %d, want 1 (resync)", got)
	}
}

func TestTrailThirtySecondSpacing(t *testing.T) {
	s := NewStore()
	icao := [3]byte{0x40, 0x6B, 0x90}
	// A burst of position fixes within a few seconds: only ONE
	// breadcrumb is laid down (the 5 s spacing rule).
	s.Decode(AppendCRC24(encodePosFrame(icao, 13.70, 100.60, 35000, true)))
	s.Decode(AppendCRC24(encodePosFrame(icao, 13.701, 100.601, 35000, false)))
	s.Decode(AppendCRC24(encodePosFrame(icao, 13.702, 100.602, 35000, true)))
	ps := s.Planes()
	if len(ps) != 1 {
		t.Fatalf("planes %d", len(ps))
	}
	if n := len(ps[0].Trail); n != 1 {
		t.Fatalf("trail has %d dots after a same-second burst, want 1 (5 s spacing)", n)
	}
	// Latest position still tracks the newest fix (CPR round-trip
	// carries a few-metre epsilon).
	if math.Abs(ps[0].Lat-13.702) > 0.001 {
		t.Fatalf("position did not update: %.4f", ps[0].Lat)
	}
}
