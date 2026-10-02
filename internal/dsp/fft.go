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

// SpectrumTap keeps the newest len(buf) samples for the display
// thread. Push happens from the DSP goroutine; Snapshot from the UI
// goroutine. It is a true ring (write index, no shifting) so Push
// stays O(len(block)) even with the 64k-sample raw history.
type SpectrumTap struct {
	mu    sync.Mutex
	buf   []complex128
	head  int    // next write position
	total uint64 // samples ever pushed (for partial-fill padding)
	gen   uint64 // bumped on every Push
}

// TapLen is the retained history of the default (IF) tap: enough
// samples for the device UI's finest zoom FFT (16k points at 256
// ksps → 15.6 Hz bins).
const TapLen = 16384

// RawTapLen is the full-rate tap history: 64k points give the web
// spectrum 4× finer resolution than TapLen (31 Hz FFT bins at 2.048
// Msps).
const RawTapLen = 65536

func NewSpectrumTap() *SpectrumTap { return NewSpectrumTapN(TapLen) }

// NewSpectrumTapN keeps the newest n samples.
func NewSpectrumTapN(n int) *SpectrumTap {
	return &SpectrumTap{buf: make([]complex128, n)}
}

// Push records the latest samples of block.
func (t *SpectrumTap) Push(block []complex128) {
	n := len(block)
	if n == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	N := len(t.buf)
	if n >= N {
		copy(t.buf, block[n-N:])
		t.head = 0
	} else {
		c := copy(t.buf[t.head:], block)
		copy(t.buf, block[c:])
		t.head = (t.head + n) % N
	}
	t.total += uint64(n)
	t.gen++
}

// SnapshotN copies the NEWEST min(len(dst), capacity) samples into dst
// in chronological order (len(dst) should be ≤ capacity — powers of
// two for the UI's FFT) and returns the generation. If fewer samples
// were ever pushed than requested, the front is zero-padded. Returns
// gen 0 if nothing has been pushed yet.
func (t *SpectrumTap) SnapshotN(dst []complex128) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	N := len(t.buf)
	m := len(dst)
	if m > N {
		m = N
	}
	avail := t.total
	if avail > uint64(N) {
		avail = uint64(N)
	}
	if uint64(m) > avail {
		pad := m - int(avail)
		for i := 0; i < pad; i++ {
			dst[i] = 0
		}
		dst = dst[pad:]
		m = int(avail)
	}
	if m == 0 {
		return t.gen
	}
	start := t.head - m
	if start < 0 {
		start += N
	}
	if c := copy(dst, t.buf[start:]); c < m {
		copy(dst[c:], t.buf)
	}
	return t.gen
}

// Snapshot copies the newest len(dst) samples and returns the
// generation. Returns gen 0 if nothing has been pushed yet.
func (t *SpectrumTap) Snapshot(dst []complex128) uint64 {
	return t.SnapshotN(dst)
}
