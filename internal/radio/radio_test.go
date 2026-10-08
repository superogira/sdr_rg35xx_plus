// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package radio

import (
	"testing"

	"sdr35/internal/dsp"
)

func TestGainDbMapping(t *testing.T) {
	cases := []struct {
		db   float64
		want int
	}{
		{0, 0},     // 0.0 dB
		{40, 22},   // 40 → 40.2 dB (the value from the user's ini)
		{30, 16},   // 30 → 29.7 dB (web project default)
		{49.6, 28}, // max of the table
		{99, 28},   // over the top → clamped to max index
		{-5, 28},   // handled as AGC upstream; mapping itself clamps low→0? no:
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

// SetPpm clamps to the ±120 window the menu expects; the stored value
// also survives via the session snapshot (applied at every reconnect).
func TestPpmClamp(t *testing.T) {
	r := New("x:1", 100_000_000, dsp.ModeAM, 0, nil)
	for _, c := range []struct{ in, want int }{
		{0, 0}, {5, 5}, {999, 120}, {-999, -120}, {120, 120}, {-120, -120},
	} {
		r.SetPpm(c.in)
		if got := r.Ppm(); got != c.want {
			t.Fatalf("SetPpm(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPpmOffToggle(t *testing.T) {
	r := New("x:1", 100_000_000, dsp.ModeAM, 0, nil)
	if r.PpmOff() {
		t.Fatal("default should not be off")
	}
	r.SetPpmOff(true)
	if !r.PpmOff() || r.Ppm() != 0 {
		t.Fatalf("off=%v ppm=%d", r.PpmOff(), r.Ppm())
	}
	r.SetPpm(30)
	if !r.PpmOff() || r.Ppm() != 30 {
		t.Fatalf("off must not clear the stored ppm: off=%v ppm=%d", r.PpmOff(), r.Ppm())
	}
}
