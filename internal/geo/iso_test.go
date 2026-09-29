package geo

import "testing"

func TestNameISOCoversDXCC(t *testing.T) {
	for _, name := range dxcc {
		if _, ok := nameISO[name]; !ok {
			t.Errorf("nameISO missing %q", name)
		}
	}
	for _, c := range []struct{ call, want string }{
		{"HS0ZLG", "TH"}, {"JA1ABC", "JP"}, {"W1AW", "US"},
		{"G4XYZ", "GB"}, {"9V1AB", "SG"}, {"MM0ABC", "GB"},
	} {
		if got := CountryISO(c.call); got != c.want {
			t.Errorf("CountryISO(%s)=%q want %q", c.call, got, c.want)
		}
	}
}
