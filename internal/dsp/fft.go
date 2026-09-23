package dsp

import (
	"math"
	"sync"
)

// FFT computes the DFT of x in place (n must be a power of two). This is a
// plain iterative radix-2 Cooley-Tukey — 512-point at 30 fps is nothing.
func FFT(re, im []float64) {
	n := len(re)
	if n&(n-1) != 0 {
		panic("FFT length must be a power of two")
	}
	// Bit-reversal permutation.
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for length := 2; length <= n; length <<= 1 {
		ang := -2 * math.Pi / float64(length)
		for i := 0; i < n; i += length {
			for k := 0; k < length/2; k++ {
				wr := math.Cos(ang * float64(k))
				wi := math.Sin(ang * float64(k))
				u := complex(re[i+k], im[i+k])
				v := complex(re[i+k+length/2]*wr-im[i+k+length/2]*wi,
					re[i+k+length/2]*wi+im[i+k+length/2]*wr)
				re[i+k], im[i+k] = real(u+v), imag(u+v)
				re[i+k+length/2], im[i+k+length/2] = real(u-v), imag(u-v)
			}
		}
	}
}

// HannWindow applies a Hann window in place.
func HannWindow(re, im []float64) {
	n := len(re)
	for i := range re {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
		re[i] *= w
		im[i] *= w
	}
}

// SpectrumTap keeps the newest TapLen IF samples for the display thread.
// Push happens from the DSP goroutine; Snapshot from the UI goroutine.
type SpectrumTap struct {
	mu  sync.Mutex
	buf []complex128
	gen uint64 // bumped on every Push
}

// TapLen is the retained history: enough samples for the finest zoom
// FFT (16k points at 256 ksps → 15.6 Hz bins).
const TapLen = 16384

func NewSpectrumTap() *SpectrumTap {
	return &SpectrumTap{buf: make([]complex128, TapLen)}
}

// Push records the latest TapLen samples of block.
func (t *SpectrumTap) Push(block []complex128) {
	n := len(block)
	if n == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n >= TapLen {
		copy(t.buf, block[n-TapLen:])
	} else {
		copy(t.buf, t.buf[n:])
		copy(t.buf[TapLen-n:], block)
	}
	t.gen++
}

// SnapshotN copies the NEWEST len(dst) samples into dst (len(dst) must
// be ≤ TapLen — powers of two for the UI's FFT) and returns the
// generation. The newest samples sit at the ring's tail.
func (t *SpectrumTap) SnapshotN(dst []complex128) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	copy(dst, t.buf[len(t.buf)-len(dst):])
	return t.gen
}

// Snapshot copies the newest len(dst) samples and returns the
// generation. Returns gen 0 if nothing has been pushed yet.
func (t *SpectrumTap) Snapshot(dst []complex128) uint64 {
	return t.SnapshotN(dst)
}
