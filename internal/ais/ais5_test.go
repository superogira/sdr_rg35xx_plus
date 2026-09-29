package ais

import "testing"

func buildType5(mmsi uint32, name string) []string {
	b := field(5, 6) + field(0, 2) + field(mmsi, 30) + field(0, 2) + field(0, 30) + tfield("AB1234", 7) + tfield(name, 20)
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
	}
}
