// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"image/color"

	"sdr35/internal/i18n"
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

// DrawDeepCWLog draws the neural (DeepCW) decoder's rolling text in the
// same corner as the classic CW window, stacked above it so both can
// show at once. Orange accents mark it as the AI engine.
func (u *UI) DrawDeepCWLog(text string) {
	tf := Face(11, false)
	const lh = 15
	lines := splitLines(text, 40)
	maxShow := 2
	if len(lines) > maxShow {
		lines = lines[len(lines)-maxShow:]
	}
	pw := 330
	ph := len(lines)*lh + 8
	px := u.W - pw - 4
	// stacked above the classic CW window's slot (its max height 38 +
	// the 4 px gap), so the two never overlap when both run
	py := u.WaterfallRows - 4 - 16 - 4 - 18 - 4 - ph - 4 - 42
	if py < 4 {
		py = 4
	}
	u.fillBlend(px, py, pw, ph, 0, 0, 0, 180)
	tf.DrawString(u.img, color.RGBA{255, 165, 60, 255}, px+4, py+13, "CW AI")
	if len(lines) == 0 {
		return
	}
	x0 := px + 52
	for i, ln := range lines {
		y := py + 14 + i*lh
		for tf.TextWidth(ln) > px+pw-6-x0 && len(ln) > 4 {
			ln = ln[:len(ln)-2] + "…"
		}
		tf.DrawString(u.img, color.RGBA{255, 230, 190, 255}, x0, y, ln)
	}
}

// DrawCWLogFull renders a full-screen, scrollable CW history split into
// two panels: the classic decoder on the left, the neural (DeepCW)
// decoder on the right. scroll is how many lines are hidden below the
// view in BOTH panels (0 = newest at the bottom), matching the FT8 big
// window's convention.
func (u *UI) DrawCWLogFull(classic, ai string, scroll int) {
	lh := 15
	x, y := 12, 8
	w := u.W - 24
	h := u.WaterfallRows - 16
	if h < 6*lh {
		return
	}
	u.fillBlend(x, y, w, h, 0, 0, 0, 215)
	u.fillBlend(x, y, w, 2, 200, 60, 60, 255)
	u.fillBlend(x, y+h-2, w, 2, 200, 60, 60, 255)
	// centre divider
	u.fillBlend(x+w/2, y+2, 1, h-4, 90, 90, 90, 255)

	titleF := Face(14, true)
	lineF := Face(12, false)
	hintF := Face(11, false)
	white := color.RGBA{235, 235, 235, 255}
	gray := color.RGBA{150, 180, 150, 255}
	orange := color.RGBA{255, 165, 60, 255}
	green := color.RGBA{180, 230, 180, 255}

	titleF.DrawString(u.img, white, x+10, y+20, "CW")
	pos := fmt.Sprintf("%d+%d", len(splitLines(classic, 34)), len(splitLines(ai, 34)))
	lineF.DrawString(u.img, gray, x+w-10-lineF.TextWidth(pos), y+20, pos)

	panel := func(px, pw int, text string, hdr string, hdrCol, txtCol color.RGBA) {
		lineF.DrawString(u.img, hdrCol, px+6, y+20+lh, hdr)
		lines := splitLines(text, 34)
		vis := (h - 3*lh - 14) / lh
		if vis < 1 {
			vis = 1
		}
		// clamp BEFORE the vis fill: an empty panel must not index
		// past its (zero) lines
		bottom := len(lines) - scroll
		if bottom > len(lines) {
			bottom = len(lines)
		}
		if bottom < 0 {
			bottom = 0
		}
		top := bottom - vis
		if top < 0 {
			top = 0
		}
		for i := top; i < bottom; i++ {
			yy := y + 2*lh + 14 + (i-top)*lh
			ln := lines[i]
			for lineF.TextWidth(ln) > pw-12 && len(ln) > 4 {
				ln = ln[:len(ln)-2] + "…"
			}
			lineF.DrawString(u.img, txtCol, px+6, yy, ln)
		}
	}
	panel(x+4, w/2-10, classic, "CW", color.RGBA{120, 220, 255, 255}, green)
	panel(x+w/2+6, w/2-10, ai, "CW AI", orange, color.RGBA{255, 230, 190, 255})
	hintF.DrawString(u.img, gray, x+10, y+h-12, i18n.T("ft8_scroll"))
}
