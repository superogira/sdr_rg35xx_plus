// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"math"
	"testing"
)

// RF → real USB chain → CW decoder (the full app path).
func TestCWThroughUSBChain(t *testing.T) {
	SetIQRate(2048000)
	ch := NewChain(ModeUSB, nil, nil)
	dec := NewCWDecoder()
	ch.SetCWDecoder(dec)
	_, keyed := demoCWAudio()
	const block = 51200
	iq := make([]byte, 2*block)
	var audio []float32
	pos, ph := 0, 0.0
	for sent := 0; sent < 2*len(keyed)*IQRate/WefaxRate; sent += block {
		for i := 0; i < block; i++ {
			if keyed[(pos/1024)%len(keyed)] {
				ph += 2 * math.Pi * 700 / float64(IQRate)
				iq[2*i] = byte(clampU8(0.30*math.Cos(ph)*119 + 127.5))
				iq[2*i+1] = byte(clampU8(0.30*math.Sin(ph)*119 + 127.5))
			} else {
				iq[2*i], iq[2*i+1] = 127, 127
			}
			pos += WefaxRate * 1024 / IQRate
		}
		audio = audio[:0]
		ch.Process(iq, &audio)
	}
	got := dec.Text()
	if len(got) < 30 || got[:9] != "VVV DE TE" {
		t.Fatalf("chain decode: %q", got)
	}
	t.Logf("chain decode: %q (%.1f wpm)", got, dec.WPM())
}
