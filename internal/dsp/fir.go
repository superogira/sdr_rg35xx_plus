package dsp

import "math"

// DesignLowpass returns a Hamming-windowed sinc low-pass FIR (linear phase,
// symmetric). cutoff is in Hz at sampleRate; the -6 dB point lands at the
// cutoff and the transition band is roughly 3.3/taps of the sample rate.
func DesignLowpass(taps int, cutoff, sampleRate float64) []float64 {
	if taps%2 == 0 {
		taps++ // keep the filter odd so it has a true center tap
	}
	h := make([]float64, taps)
	fc := cutoff / sampleRate // normalized to Nyquist=1 → cycles/sample = fc/2
	var sum float64
	for i := 0; i < taps; i++ {
		m := float64(i - (taps-1)/2)
		v := 2 * fc * sinc(2*fc*m) // 2*fc pairs with sinc(x)=sin(πx)/(πx) so gain=1 at DC
		v *= hamming(i, taps)
		h[i] = v
		sum += v
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func hamming(i, n int) float64 {
	return 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(n-1))
}

// complexFIRDecim low-pass filters the complex input and decimates by D,
// appending only the kept outputs to *out. hist carries the previous
// len(taps)-1 samples between calls; it is grown/rotated in place.
//
// Output j uses inputs x[jD + D - 1 - k] for k in [0, len(taps)), reaching
// into hist for the negative indices, so the cost is outputs×taps — the
// dropped samples are never filtered.
func complexFIRDecim(taps []float64, hist *[]complex128, D int, in []complex128, out *[]complex128) {
	L := len(taps)
	H := *hist
	if cap(H) < L-1 {
		H = make([]complex128, L-1)
	}
	H = H[:L-1]

	nOut := len(in) / D
	base := len(*out)
	*out = growComplex(*out, base+nOut)[0 : base+nOut]

	for j := 0; j < nOut; j++ {
		newest := j*D + D - 1
		var acc complex128
		for k := 0; k < L; k++ {
			idx := newest - k
			var x complex128
			if idx >= 0 {
				x = in[idx]
			} else {
				x = H[L-1+idx]
			}
			acc += complex(real(x)*taps[k], imag(x)*taps[k])
		}
		(*out)[base+j] = acc
	}

	// Keep the last L-1 samples of (hist + in).
	if len(in) >= L-1 {
		copy(H, in[len(in)-(L-1):])
	} else {
		keep := L - 1 - len(in)
		copy(H, H[len(H)-keep:])
		copy(H[keep:], in)
	}
	*hist = H
}

// realFIRDecim is complexFIRDecim for real inputs/outputs.
func realFIRDecim(taps []float64, hist *[]float64, D int, in []float64, out *[]float64) {
	L := len(taps)
	H := *hist
	if cap(H) < L-1 {
		H = make([]float64, L-1)
	}
	H = H[:L-1]

	nOut := len(in) / D
	base := len(*out)
	*out = growFloat(*out, base+nOut)[0 : base+nOut]

	for j := 0; j < nOut; j++ {
		newest := j*D + D - 1
		var acc float64
		for k := 0; k < L; k++ {
			idx := newest - k
			var x float64
			if idx >= 0 {
				x = in[idx]
			} else {
				x = H[L-1+idx]
			}
			acc += x * taps[k]
		}
		(*out)[base+j] = acc
	}

	if len(in) >= L-1 {
		copy(H, in[len(in)-(L-1):])
	} else {
		keep := L - 1 - len(in)
		copy(H, H[len(H)-keep:])
		copy(H[keep:], in)
	}
	*hist = H
}

// DCBlocker removes the rtl_tcp DC spike with the standard one-pole IIR
// y = x - x1 + R*y1 (R below 1); a few-hundred-Hz notch at 960 ksps.
type DCBlocker struct {
	x1, y1 complex128
	r      float64
}

func NewDCBlocker(sampleRate float64) DCBlocker {
	// Notch around 2 kHz at IQRate.
	r := 1 - 2*math.Pi*2000/sampleRate
	if r < 0.9 {
		r = 0.9
	}
	return DCBlocker{r: r}
}

func (d *DCBlocker) Step(x complex128) complex128 {
	y := x - d.x1 + complex(d.r, 0)*d.y1
	d.x1, d.y1 = x, y
	return y
}
