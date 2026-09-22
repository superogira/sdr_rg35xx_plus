package radio

import "testing"

func TestGainDbMapping(t *testing.T) {
	cases := []struct {
		db   float64
		want int
	}{
		{0, 0},       // 0.0 dB
		{40, 22},     // 40 → 40.2 dB (the value from the user's ini)
		{30, 16},     // 30 → 29.7 dB (web project default)
		{49.6, 28},   // max of the table
		{99, 28},     // over the top → clamped to max index
		{-5, 28},     // handled as AGC upstream; mapping itself clamps low→0? no:
	}
	// -5 dB maps to nearest (0.0) — the AGC decision happens in New.
	cases[len(cases)-1] = struct {
		db   float64
		want int
	}{-5, 0}
	for _, c := range cases {
		if got := GainDbToIndex(c.db); got != c.want {
			t.Errorf("GainDbToIndex(%v) = %d (%.1f dB), want %d", c.db, got, GainIndexDb(got), c.want)
		}
	}
	if len(r828dGains) != 29 {
		t.Errorf("gain table has %d entries, expected 29 (RTL-SDR Blog V4)", len(r828dGains))
	}
}
