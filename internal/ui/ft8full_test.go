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
		u.DrawFT8LogFull(entries, c.scroll, "")
	}
	_ = frame
}

// The mini decode window must fit a realistic worst-case line: decoded
// text + gap + annotation + flag inside the panel width.
func TestFT8MiniWindowFits(t *testing.T) {
	tf := Face(11, false)
	pw := 440
	cases := []struct{ text, anno string }{
		{"CQ PY2ABCD GG66omp", "Brazil 16500km"},
		{"KH6/KM4YEG JA3ABC PM74", "USA 11300km"},
		{"TU4X EA8ABC IL18", "Ivory Coast 11200km"},
	}
	for _, c := range cases {
		txtW := tf.TextWidth(c.text)
		annoW := tf.TextWidth(c.anno)
		need := 106 + txtW + 8 + annoW + 3 + 18 + 6 // columns..gap..anno..gap..flag..pad
		if need > pw {
			t.Errorf("%q + %q needs %dpx > %dpx", c.text, c.anno, need, pw)
		}
	}
}
