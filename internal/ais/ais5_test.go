package ais

import "testing"

func buildType5(mmsi uint32, name string) []string {
	// Full frame out to the ETA so the new fields have real bits:
	// imo 1234567, callsign, shiptype 70 (cargo), dims A=55 B=10 C=4
	// D=10 (65×14 m), draught 30 dm, destination, ETA 05-02 14:00.
	b := field(5, 6) + field(0, 2) + field(mmsi, 30) + field(0, 2) + field(1234567, 30) + tfield("HSB3451", 7) + tfield(name, 20) +
		field(70, 8) + field(55, 9) + field(10, 9) + field(4, 6) + field(10, 6) + field(0, 4) + field(30, 8) + field(0, 1) + field(0, 19) +
		tfield("SRIRACHA", 20) + field(5, 4) + field(2, 5) + field(14, 5) + field(0, 6)
	enc := encodeBits(b)
	p1 := enc[:len(enc)/2]
	p2 := enc[len(enc)/2:]
	return []string{
		"!AIVDM,2,1,7,B," + p1 + ",0*00",
		"!AIVDM,2,2,7,B," + p2 + ",2*00",
	}
}

func TestType5RoundTrip(t *testing.T) {
	s := NewStore()
	for _, ln := range buildType5(567001234, "MV CHAO PHRAYA") {
		s.decodeT(ln)
	}
	for _, sh := range s.Ships() {
		if sh.MMSI != "567001234" || sh.Name != "MV CHAO PHRAYA" {
			t.Fatalf("mmsi=%s name=%q", sh.MMSI, sh.Name)
		}
		if sh.Callsign != "HSB3451" || sh.Imo != 1234567 {
			t.Fatalf("callsign=%q imo=%d", sh.Callsign, sh.Imo)
		}
		if sh.ShipType != 70 || sh.Draught != 3 {
			t.Fatalf("shiptype=%d draught=%v", sh.ShipType, sh.Draught)
		}
		if sh.DimA+sh.DimB != 65 || sh.DimC+sh.DimD != 14 {
			t.Fatalf("dims %d+%d x %d+%d", sh.DimA, sh.DimB, sh.DimC, sh.DimD)
		}
		if sh.Destination != "SRIRACHA" || sh.EtaText != "05-02 14:00" {
			t.Fatalf("dest=%q eta=%q", sh.Destination, sh.EtaText)
		}
	}
}
