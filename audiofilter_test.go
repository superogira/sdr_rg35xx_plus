package main

import "testing"

// TestAudioFilterPresets: preset matching and stepping — the one-touch
// "audio filter" row (narrow/normal/wide) sets both corners, custom
// corners (hand-adjusted HP/LP) match no preset and pressing either
// way lands on normal.
func TestAudioFilterPresets(t *testing.T) {
	// Preset corners are internally consistent and distinct.
	seen := map[int]bool{}
	for i, p := range audioFilterPresets {
		if p.hpHz <= 0 || p.lpHz <= 0 {
			t.Fatalf("preset %d has a disabled corner (hp %d lp %d)", i, p.hpHz, p.lpHz)
		}
		key := p.hpHz<<16 | p.lpHz
		if seen[key] {
			t.Fatalf("presets %d duplicates corners hp %d lp %d", i, p.hpHz, p.lpHz)
		}
		seen[key] = true
		if audioFilterPresetIndex(p.hpHz, p.lpHz) != i {
			t.Fatalf("preset %d does not match its own corners", i)
		}
	}
	// Narrow < normal < wide in both corners.
	if !(audioFilterPresets[0].hpHz > audioFilterPresets[1].hpHz &&
		audioFilterPresets[1].hpHz > audioFilterPresets[2].hpHz &&
		audioFilterPresets[0].lpHz < audioFilterPresets[1].lpHz &&
		audioFilterPresets[1].lpHz < audioFilterPresets[2].lpHz) {
		t.Fatalf("presets not ordered narrow→normal→wide: %+v", audioFilterPresets)
	}

	// Off corners (0/0) are custom, and either direction lands on normal.
	if audioFilterPresetIndex(0, 0) != -1 {
		t.Fatal("off corners must read custom")
	}
	for _, dir := range []int{-1, 1} {
		if got := nextAudioFilterPreset(0, 0, dir); got != 1 {
			t.Fatalf("custom + dir %d → preset %d, want 1 (normal)", dir, got)
		}
	}
	// From a preset, dir moves and wraps.
	if got := nextAudioFilterPreset(400, 1700, 1); got != 1 {
		t.Fatalf("narrow + right → %d, want normal", got)
	}
	if got := nextAudioFilterPreset(100, 3000, 1); got != 0 {
		t.Fatalf("wide + right → %d, want narrow (wrap)", got)
	}
	if got := nextAudioFilterPreset(300, 2400, -1); got != 0 {
		t.Fatalf("normal + left → %d, want narrow", got)
	}
}
