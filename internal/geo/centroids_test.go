package geo

import "testing"

// TestCentroidsCoverDXCC: every country the prefix table can return
// must have a centroid — a missing name silently drops map markers.
func TestCentroidsCoverDXCC(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range dxcc {
		if seen[name] {
			continue
		}
		seen[name] = true
		ll, ok := countryCenter[name]
		if !ok {
			t.Errorf("dxcc country %q has no centroid", name)
			continue
		}
		lat, lon := ll[0], ll[1]
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			t.Errorf("country %q centroid out of range: %v %v", name, lat, lon)
		}
	}
}

// TestCountryLatLon: known calls land on the right country; garbage
// calls report !ok.
func TestCountryLatLon(t *testing.T) {
	cases := []struct {
		call     string
		lat, lon float64
	}{
		{"HS0ZKO", 15, 101},    // Thailand
		{"JA1ABC", 36, 138},    // Japan
		{"W1AW", 39.8, -98.6},  // USA
		{"VK2GR", -25, 134},    // Australia
		{"9A1XYZ", 45.1, 15.2}, // Croatia
	}
	for _, c := range cases {
		lat, lon, ok := CountryLatLon(c.call)
		if !ok {
			t.Errorf("%s: no position (country missing from centroid table?)", c.call)
			continue
		}
		if d := lat - c.lat; d < -1 || d > 1 {
			t.Errorf("%s: lat %.1f, want ~%.1f", c.call, lat, c.lat)
		}
		if d := lon - c.lon; d < -1 || d > 1 {
			t.Errorf("%s: lon %.1f, want ~%.1f", c.call, lon, c.lon)
		}
	}
	if _, _, ok := CountryLatLon("ZZ1ZZ"); ok {
		t.Errorf("garbage call resolved to a position")
	}
}
