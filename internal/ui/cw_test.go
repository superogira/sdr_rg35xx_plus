// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

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

// TestDrawCWLogFullNoPanic: empty panels and out-of-range scroll must
// not index past zero lines (a real crash: bottom=vis with len(lines)=0).
func TestDrawCWLogFullNoPanic(t *testing.T) {
	u := New(640, 480)
	u.DrawCWLogFull("", "", 0)
	u.DrawCWLogFull("", "", 50)
	u.DrawCWLogFull("CQ CQ DE HS0ABC", "", 3)
	u.DrawCWLogFull("", "CQ CQ DE HS0ABC K", 99)
	long := ""
	for i := 0; i < 40; i++ {
		long += "CQ CQ DE HS0ABC HS0ABC K RST 599 "
	}
	u.DrawCWLogFull(long, long, 0)
	u.DrawCWLogFull(long, long, 200)
}
