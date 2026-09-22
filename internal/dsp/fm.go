package dsp

import "math"

// FMDemod is a polar discriminator: the phase difference between
// consecutive IF samples is the instantaneous frequency. Output is in Hz
// relative to the channel center.
type FMDemod struct {
	rate float64 // samples per second of the input stream
	prev complex128
}

// Step returns the instantaneous frequency for sample z.
func (f *FMDemod) Step(z complex128) float64 {
	prod := z * conj(f.prev)
	f.prev = z
	r := f.rate
	if r == 0 {
		r = float64(IF2Rate)
	}
	return math.Atan2(imag(prod), real(prod)) * r / (2 * math.Pi)
}

func conj(z complex128) complex128 { return complex(real(z), -imag(z)) }

// Deemph is a one-pole de-emphasis (or the matching pre-emphasis curve)
// with time constant tau at sampleRate.
type Deemph struct {
	a  float64 // feedback coefficient
	y1 float64
}

func NewDeemph(tau, sampleRate float64) *Deemph {
	return &Deemph{a: math.Exp(-1 / (tau * sampleRate))}
}

func (d *Deemph) Step(x float64) float64 {
	d.y1 = d.y1*d.a + (1-d.a)*x
	return d.y1
}
