package ais

import (
	"fmt"
	"math"
	"testing"
)

// encodeBits packs a bit string into AIVDM 6-bit payload characters.
func encodeBits(bits string) string {
	var out []byte
	for len(out)*6 < len(bits) {
		var v byte
		for k := 0; k < 6; k++ {
			v <<= 1
			idx := len(out)*6 + k
			if idx < len(bits) && bits[idx] == '1' {
				v |= 1
			}
		}
		if v < 40 {
			out = append(out, v+48)
		} else {
			out = append(out, v+56)
		}
	}
	return string(out)
}

func field(v uint32, n int) string {
	b := make([]byte, n)
	for k := 0; k < n; k++ {
		b[k] = '0'
	}
	for k := 0; k < n; k++ {
		if v&(1<<(n-1-k)) != 0 {
			b[k] = '1'
		}
	}
	return string(b)
}

func sfield(v int32, n int) string {
	return field(uint32(v)&((1<<n)-1), n)
}

func tfield(s string, chars int) string {
	bits := ""
	s = s + "@@@@@@@@@@@@@@@@@@@@@@@@"
	for k := 0; k < chars; k++ {
		c := s[k]
		// The 6-bit text alphabet: ASCII 48-63 → v-48, 64-95 → v-32
		// (uppercase letters live at v 33-58, transmitted as the
		// backtick-lowercase range).
		var v byte
		switch {
		case c >= 48 && c <= 63:
			v = c - 48
		case c >= 64 && c <= 95:
			v = c - 32
		default:
			v = 32 // space and anything exotic → '@'
		}
		bits += field(uint32(v), 6)
	}
	return bits
}

// buildPos assembles a type-1 (class A position) sentence.
func buildPos(mmsi uint32, lat, lon, sogKt, cogDeg float64) string {
	b := field(1, 6) + field(0, 2) + field(mmsi, 30) +
		field(0, 4) + // nav status
		field(0, 8) + // ROT
		field(uint32(math.Round(sogKt*10)), 10) +
		field(1, 1) + // accuracy
		sfield(int32(math.Round(lon*600000)), 28) +
		sfield(int32(math.Round(lat*600000)), 27) +
		field(uint32(math.Round(cogDeg*10)), 12) +
		field(511, 9) + // heading N/A
		field(60, 6) // timestamp
	payload := encodeBits(b)
	return fmt.Sprintf("!AIVDM,1,1,,A,%s,0*00", payload)
}

func buildName(mmsi uint32, name string) string {
	b := field(24, 6) + field(0, 2) + field(mmsi, 30) + field(0, 2) + tfield(name, 20)
	payload := encodeBits(b)
	return fmt.Sprintf("!AIVDM,1,1,,A,%s,0*00", payload)
}

func TestDecodePositionAndName(t *testing.T) {
	s := NewStore()
	s.decodeT(buildPos(567890123, 13.5520, 100.5120, 12.5, 87.3))
	s.decodeT(buildName(567890123, "MV BANGKOK"))
	ships := s.Ships()
	if len(ships) != 1 {
		t.Fatalf("ships %d", len(ships))
	}
	sh := ships[0]
	if sh.MMSI != "567890123" {
		t.Fatalf("mmsi %s", sh.MMSI)
	}
	if math.Abs(sh.Lat-13.5520) > 0.0005 || math.Abs(sh.Lon-100.5120) > 0.0005 {
		t.Fatalf("position %.5f %.5f", sh.Lat, sh.Lon)
	}
	if math.Abs(sh.SogKt-12.5) > 0.1 || math.Abs(sh.CogDeg-87.3) > 0.1 {
		t.Fatalf("sog %f cog %f", sh.SogKt, sh.CogDeg)
	}
	if sh.Name != "MV BANGKOK" {
		t.Fatalf("name %q", sh.Name)
	}
}

func TestDecodeClassB(t *testing.T) {
	s := NewStore()
	// Type 18 position.
	b := field(18, 6) + field(0, 2) + field(123456789, 30) + field(0, 8) +
		field(uint32(73), 10) + field(1, 1) +
		sfield(int32(math.Round(100.6123*600000)), 28) +
		sfield(int32(math.Round(9.1234*600000)), 27) +
		field(uint32(450), 12) + field(511, 9)
	s.decodeT(fmt.Sprintf("!AIVDM,1,1,,B,%s,0*00", encodeBits(b)))
	// Type 24A name.
	s.decodeT(buildName(123456789, "FERRY9"))
	ships := s.Ships()
	if len(ships) != 1 || !ships[0].HasPos {
		t.Fatalf("class B: %+v", ships)
	}
	if math.Abs(ships[0].Lat-9.1234) > 0.0005 || math.Abs(ships[0].Lon-100.6123) > 0.0005 {
		t.Fatalf("class B pos %.4f %.4f", ships[0].Lat, ships[0].Lon)
	}
	if ships[0].Name != "FERRY9" {
		t.Fatalf("class B name %q", ships[0].Name)
	}
}

func TestDecodeIgnoresJunk(t *testing.T) {
	s := NewStore()
	for _, ln := range []string{
		"",
		"$GPGGA,1234",
		"!AIVDM,2,1,...", // multi-fragment skipped
		"garbage without commas",
		"!AIVDM,1,1,,A,###,0*00", // bad payload chars
	} {
		s.decodeT(ln)
	}
	if len(s.Ships()) != 0 {
		t.Fatalf("junk decoded: %+v", s.Ships())
	}
}

// buildAtoN assembles a type 21 (aid-to-navigation) or type 6 (assigned
// mode base station) sentence with position at the given bit offsets.
func buildAtoN(typ uint32, mmsi uint32, name string, lat, lon float64, latOff, lonOff int) string {
	bits := field(typ, 6) + field(0, 2) + field(mmsi, 30)
	// Pad to latOff, then 28+28 bits of position, then filler.
	for len(bits) < latOff {
		bits += "0"
	}
	bits += sfield(int32(math.Round(lat*600000)), 28) + sfield(int32(math.Round(lon*600000)), 28)
	for len(bits)%6 != 0 {
		bits += "0"
	}
	if name != "" && latOff > 43 {
		// Type 21 puts the name at bit 43 — rebuild with it.
		bits = field(typ, 6) + field(0, 2) + field(mmsi, 30) + tfield(name, 20) + "00" + "0"
		for len(bits) < lonOff {
			bits += "0"
		}
		bits += sfield(int32(math.Round(lon*600000)), 28) + sfield(int32(math.Round(lat*600000)), 28)
		for len(bits)%6 != 0 {
			bits += "0"
		}
	}
	return fmt.Sprintf("!AIVDM,1,1,,A,%s,0*00", encodeBits(bits))
}

func TestDecodeAtoNTypes(t *testing.T) {
	// Type 21: name at 43, lon at 165, lat at 193.
	s := NewStore()
	s.decodeT(buildAtoN(21, 992190761, "BANGKOK LIGHT", 13.7210, 100.5120, 193, 165))
	// Type 6: lat at 72, lon at 100.
	s.decodeT(buildAtoN(6, 993190762, "", 7.5000, 100.4000, 72, 100))
	var aids, ships int
	for _, sh := range s.Ships() {
		if sh.AtoN {
			aids++
			if math.Abs(sh.Lat-13.7210) > 0.001 && math.Abs(sh.Lat-7.5) > 0.001 {
				t.Fatalf("aid %s position %.4f %.4f", sh.MMSI, sh.Lat, sh.Lon)
			}
		} else {
			ships++
		}
	}
	if aids != 2 {
		t.Fatalf("expected 2 aids-to-navigation, got %d (ships=%d)", aids, ships)
	}
	if ships != 0 {
		t.Fatalf("AtoN entries counted as vessels: %d", ships)
	}
}
