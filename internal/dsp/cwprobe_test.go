// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"strings"
	"testing"
)

func TestCWProbe35(t *testing.T) {
	for _, wpm := range []float64{25, 30, 35} {
		g := newCWGen(700, wpm, 0, 0, 1)
		g.text("CQ CQ DE HS0AB")
		d := NewCWDecoder()
		const chunk = 1024
		var drained strings.Builder
		for off := 0; off < len(g.pending); off += chunk {
			end := off + chunk
			if end > len(g.pending) {
				end = len(g.pending)
			}
			d.Feed(g.pending[off:end])
			for _, r := range d.Take() {
				drained.WriteRune(r)
			}
		}
		t.Logf("wpm %.0f (dotN %d): %q wpm-tracked %.1f", wpm, g.dotN, drained.String(), d.WPM())
	}
}
