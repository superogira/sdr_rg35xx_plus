package ui

import (
	"testing"
)

func TestAltitudeColor(t *testing.T) {
	cases := []struct {
		ft      int
		wantHue float64
	}{
		{0, 20},
		{500, 20},
		{2000, 32.5},
		{4000, 43},
		{6000, 54},
		{8000, 72},
		{9000, 85},
		{11000, 140},
		{20000, 194.8}, // roughly mid 11k-40k
		{40000, 300},
		{51000, 360},
		{60000, 360},
	}
	for _, c := range cases {
		col := altitudeColor(c.ft)
		if col.R == 0 && col.G == 0 && col.B == 0 {
			t.Errorf("%d ft: got black", c.ft)
		}
	}
}
