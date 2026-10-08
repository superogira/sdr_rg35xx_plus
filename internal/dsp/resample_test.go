// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"math"
	"testing"
)

func TestResampleLinear48k(t *testing.T) {
	// 1 kHz complex tone at 64k in → same tone at 48k out.
	var in []complex128
	for i := 0; i < 6400; i++ {
		a := 2 * math.Pi * 1000 * float64(i) / 64000
		in = append(in, complex(math.Cos(a), math.Sin(a)))
	}
	var out []complex128
	var pos float64
	// feed in chunks to exercise cross-block state
	for len(in) > 0 {
		chunk := in
		if len(chunk) > 1000 {
			chunk = in[:1000]
		}
		var part []complex128
		resampleLinear(chunk, &pos, 48000.0/64000.0, &part)
		out = append(out, part...)
		in = in[len(chunk):]
	}
	want := len(out)
	if want < 4790 || want > 4810 {
		t.Fatalf("out len %d, want ~4800", want)
	}
	// Phase continuity: output j should sit at input time j/48000 s.
	errMax := 0.0
	for j := 100; j < len(out)-100; j++ {
		a := 2 * math.Pi * 1000 * float64(j) / 48000
		want := complex(math.Cos(a), math.Sin(a))
		d := out[j] - want
		e := math.Hypot(real(d), imag(d))
		if e > errMax {
			errMax = e
		}
	}
	if errMax > 0.05 {
		t.Fatalf("max phase error %.3f", errMax)
	}
}
