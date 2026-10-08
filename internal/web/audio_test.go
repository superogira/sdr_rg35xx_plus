// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package web

import "testing"

// µ-law encode must round-trip within its quantisation step, and the
// browser's decode table (mirrored here) must invert it.
func TestUlawRoundTrip(t *testing.T) {
	dec := make([]float32, 256)
	for i := 0; i < 256; i++ {
		b := ^byte(i) & 0xFF
		sign := b & 0x80
		exp := (b >> 4) & 7
		mant := b & 0xF
		v := ((int(mant) << 3) + 0x84) << exp
		v -= 0x84
		if sign != 0 {
			v = -v
		}
		dec[i] = float32(v) / 32768
	}
	for _, v := range []float32{0, 0.001, -0.001, 0.1, -0.1, 0.5, -0.5, 0.99, -0.99} {
		code := ulawEncode(v)
		got := dec[code]
		d := got - v
		if d < 0 {
			d = -d
		}
		// µ-law step near mid-scale is ~3% of full scale
		if d > 0.04 {
			t.Fatalf("ulaw(%v) -> %v (err %v)", v, got, d)
		}
	}
}
