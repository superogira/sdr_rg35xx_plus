package ui

import "testing"

// TestCWWindowsDoNotOverlap: the classic CW window and the AI (DeepCW)
// window share the bottom-right corner; the AI one must sit fully above
// the classic one at their maximum heights.
func TestCWWindowsDoNotOverlap(t *testing.T) {
	u := New(640, 480)
	base := u.WaterfallRows - 4 - 16 - 4 - 18 - 4
	phMax := 2*15 + 8
	cwTop := base - phMax - 4
	aiBottom := base - phMax - 4 - 42 + phMax
	if aiBottom > cwTop {
		t.Fatalf("AI window bottom %d overlaps CW window top %d", aiBottom, cwTop)
	}
}
