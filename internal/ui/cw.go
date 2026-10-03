package ui

import (
	"image/color"
)

// DrawCWLog draws the live Morse decode as a small window on the
// waterfall: the last two lines of text, plus the tracked speed.
// Bottom-right, above the clock/resource badges.
func (u *UI) DrawCWLog(text string, wpm float64) {
	tf := Face(11, false)
	const lh = 15
	lines := splitLines(text, 40) // ~40 chars/line at 11 px
	maxShow := 2
	if len(lines) > maxShow {
		lines = lines[len(lines)-maxShow:]
	}
	pw := 330
	ph := len(lines)*lh + 8
	px := u.W - pw - 4
	py := u.WaterfallRows - 4 - 16 - 4 - 18 - 4 - ph - 4
	if py < 4 {
		py = 4
	}
	u.fillBlend(px, py, pw, ph, 0, 0, 0, 180)
	hint := "CW"
	if wpm > 0 {
		hint = "CW " + itoaWpm(wpm)
	}
	tf.DrawString(u.img, color.RGBA{120, 220, 255, 255}, px+4, py+13, hint)
	if len(lines) == 0 {
		return
	}
	x0 := px + 46
	for i, ln := range lines {
		y := py + 14 + i*lh
		// clip to the window width
		for tf.TextWidth(ln) > px+pw-6-x0 && len(ln) > 4 {
			ln = ln[:len(ln)-2] + "…"
		}
		tf.DrawString(u.img, color.RGBA{180, 230, 180, 255}, x0, y, ln)
	}
}

func itoaWpm(w float64) string {
	// 1 decimal, no strconv import needed here
	s := ""
	if w >= 100 {
		s += string(rune('0' + int(w/100)%10))
	}
	if w >= 10 {
		s += string(rune('0' + int(w/10)%10))
	}
	s += string(rune('0' + int(w)%10))
	s += "w"
	return s
}

// splitLines wraps text at n runes (the decoded text is short).
func splitLines(text string, n int) []string {
	r := []rune(text)
	var out []string
	for len(r) > 0 {
		// break at the last space before n
		end := n
		if end > len(r) {
			end = len(r)
		}
		cut := end
		for i := end - 1; i > 0 && i > end-20; i-- {
			if r[i] == ' ' {
				cut = i + 1
				break
			}
		}
		out = append(out, string(r[:cut]))
		r = r[cut:]
	}
	return out
}
