// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import (
	"testing"
)

// probeBits reconstructs the exact bit sequence Modulate sends.
func probeBits(body []byte, preambleFlags int) []byte {
	fcsLo, fcsHi := FCSBytes(body)
	stuffed := append(append([]byte(nil), body...), fcsLo, fcsHi)
	var bits []byte
	pushFlag := func() {
		f := byte(0x7E)
		for i := 0; i < 8; i++ {
			bits = append(bits, (f>>i)&1)
		}
	}
	for i := 0; i < preambleFlags; i++ {
		pushFlag()
	}
	ones := 0
	for _, b := range stuffed {
		for i := 0; i < 8; i++ {
			bit := (b >> i) & 1
			bits = append(bits, bit)
			if bit == 1 {
				ones++
				if ones == 5 {
					bits = append(bits, 0)
					ones = 0
				}
			} else {
				ones = 0
			}
		}
	}
	pushFlag()
	return bits
}

// TestProbeDeframerExactBits pins the HDLC deframer on a bit-perfect
// input (no analog stage): flags, stuffing, LSB-first order and FCS
// must all line up.
func TestProbeDeframerExactBits(t *testing.T) {
	body, _ := buildTestFrame()
	bits := probeBits(body, 6)
	d := &Demodulator{}
	for _, b := range bits {
		d.stepBit(b)
	}
	f := d.TakeFrames()
	if len(f) == 0 {
		t.Fatal("deframer fails on exact bits")
	}
}
