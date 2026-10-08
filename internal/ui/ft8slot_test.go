// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"testing"
)

// TestMarkFT8SlotBackdropOnlyBehindText: the slot marker's black
// backdrop must cover the timestamp only — a full-width band blanks
// the whole waterfall row (user report). Red boundary line stays
// full-width by design.
func TestMarkFT8SlotBackdropOnlyBehindText(t *testing.T) {
	u := New(640, 480)
	u.MarkFT8Slot("2026-10-08 10:00:00")
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	px := func(x, y int) (uint32, uint32, uint32) {
		r, g, b, _ := frame.At(x, y).RGBA()
		return r >> 8, g >> 8, b >> 8
	}
	// beyond the timestamp's right edge: NO backdrop black anywhere
	// in the strip (y=5 samples glyph anti-aliasing too, so demand
	// plain not-black at a glyph-free row as well)
	// the empty waterfall's own background is (0,0,0) black — the
	// backdrop is the distinct near-black (10,10,10)
	for _, y := range []int{1, 5, 10} {
		if r, g, b := px(300, y); r >= 8 && r <= 12 && g >= 8 && g <= 12 && b >= 8 && b <= 12 {
			t.Fatalf("backdrop leaks past the text at x=300,y=%d: (%d,%d,%d)", y, r, g, b)
		}
	}
	// within the text box: backdrop black present between glyph rows
	if r, g, b := px(10, 10); r > 25 || g > 25 || b > 25 {
		t.Fatalf("backdrop missing behind text at x=10,y=10: (%d,%d,%d)", r, g, b)
	}
}
