package ui

import "testing"

// The full-window scroller must survive every history size and scroll
// position — an empty history once panicked on Select and killed the
// app.
func TestFT8LogFullBounds(t *testing.T) {
	u := New(640, 480)
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	cases := []struct {
		n, scroll int
	}{
		{0, 0}, {0, 5}, {1, 0}, {1, 3}, {5, 0}, {5, 4}, {5, 99},
		{40, 0}, {40, 8}, {40, 100}, {100, 0}, {100, 92}, {100, 200},
	}
	for _, c := range cases {
		entries := make([]FT8Entry, c.n)
		for i := range entries {
			entries[i].Time, entries[i].Text = "13:00:00", "CALL1 CALL2 EN37"
		}
		u.DrawFT8LogFull(entries, c.scroll)
	}
	_ = frame
}
