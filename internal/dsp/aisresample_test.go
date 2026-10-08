// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"testing"

	"sdr35/internal/ais"
)

// Field crash (device log, AIS RF switched on at 162 MHz): a micro TCP
// read decimated the IF2 block to 3 samples, the channel decimator then
// emitted ONE sample, and resampleLinear — entered with the fractional
// position carried negative from the previous block — indexed in[i+1]
// on a length-1 slice and panicked the whole app.
func TestResampleLinearTinyFragmentNoPanic(t *testing.T) {
	var pos float64
	var out []complex128
	// A normal block first: leaves pos in [−1, 0) as the carry.
	big := make([]complex128, 100)
	for i := range big {
		big[i] = complex(float64(i), -float64(i))
	}
	resampleLinear(big, &pos, 0.75, &out)
	n1 := len(out)
	if n1 == 0 {
		t.Fatal("no output from a full block")
	}
	// The killer: a one-sample fragment must be skipped, not indexed.
	resampleLinear(big[:1], &pos, 0.75, &out)
	resampleLinear(nil, &pos, 0.75, &out)
	// And streaming resumes cleanly afterwards.
	resampleLinear(big, &pos, 0.75, &out)
	if len(out) <= n1 {
		t.Fatalf("stream did not resume after the fragment: %d then %d", n1, len(out))
	}
}

// End-to-end through the real chain: AIS demods attached, one full
// Process block, then the 24-byte IQ fragment shape from the crash
// trace. Must not panic.
func TestFeedAISShortBlockAfterFullBlock(t *testing.T) {
	SetIQRate(1_024_000) // IF2Rate 128k — d=2, the crash configuration
	var got [][]byte
	emit := func(p []byte, ch int, levelDb float64) {
		got = append(got, append([]byte(nil), p...))
	}
	a := ais.NewChannelDemod(48000, 0, "A", emit)
	b := ais.NewChannelDemod(48000, 1, "B", emit)
	ch := NewChain(ModeNFM, nil, nil)
	ch.SetAISDemods(a, b)

	var audio []float32
	full := genFM(0.05, 800, 2500, 0.5)
	processChunked(ch, full, &audio)
	ch.Process(full[:24], &audio) // the 24-byte micro-read from the trace
	// Still alive and producing: another full block must flow through
	// feedAIS without touching the carried position wrongly.
	processChunked(ch, full, &audio)
}
