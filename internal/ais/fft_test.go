package ais

import (
	"math"
	"testing"
)

func TestFFTTone(t *testing.T) {
	n := 512
	re := make([]float64, n)
	im := make([]float64, n)
	// tone at 4800 Hz at 48k → bin 51.2: use 51 bins (5085.9 Hz) exact.
	bin := 51
	for i := 0; i < n; i++ {
		a := 2 * math.Pi * float64(bin) * float64(i) / float64(n)
		re[i] = math.Cos(a)
		im[i] = math.Sin(a)
	}
	FFT(re, im)
	best, b := 0.0, -1
	for k := 0; k < n; k++ {
		m := re[k]*re[k] + im[k]*im[k]
		if m > best {
			best, b = m, k
		}
	}
	if b != bin {
		t.Fatalf("peak bin %d, want %d", b, bin)
	}
}
