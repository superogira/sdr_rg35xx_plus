package aprs

import "testing"

func TestOwnStationFlagged(t *testing.T) {
	s := NewStore()
	s.SetOwn(Station{Call: "ME-1", Lat: 1, Lon: 2, Sym: '>'})
	all := s.All()
	if len(all) != 1 || !all[0].Own || all[0].Call != "ME-1" {
		t.Fatalf("All() = %+v", all)
	}
}
