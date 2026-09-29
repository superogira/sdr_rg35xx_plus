package geo

import "testing"

func TestICAOCountry(t *testing.T) {
	cases := map[string]string{
		"88512A": "TH",
		"A12345": "US",
		"C90123": "AU",
		"4CA87C": "IE",
		"3C1234": "DE",
		"ABCDEF": "",
		"12":     "",
	}
	for icao, want := range cases {
		if got := ICAOCountry(icao); got != want {
			t.Errorf("ICAO %s: got %q want %q", icao, got, want)
		}
	}
}

func TestMMSICountry(t *testing.T) {
	cases := map[string]string{
		"567890123": "TH",
		"366053209": "US",
		"265547250": "SE",
		"235012345": "GB",
		"123456789": "",
		"12":        "",
	}
	for mmsi, want := range cases {
		if got := MMSICountry(mmsi); got != want {
			t.Errorf("MMSI %s: got %q want %q", mmsi, got, want)
		}
	}
}
