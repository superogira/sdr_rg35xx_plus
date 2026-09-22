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

// AGC is a peak-following automatic gain control for the SSB/CW branch.
// Speech sidebands swing tens of dB between peaks and pauses while FM
// needs none of this (constant envelope), which is why SSB without AGC
// sounds much quieter than the FM modes. Fast attack tames peaks, slow
// release lifts quiet passages; the gain range is capped so noise
// between transmissions is not amplified to full scale.
type AGC struct {
	gain   float64
	env    float64
	target float64
	max    float64
}

func NewAGC() *AGC {
	return &AGC{gain: 1, target: 0.5, max: 32}
}

func (a *AGC) Step(x float64) float64 {
	// Envelope: instant rise, ~150 ms decay at 8 kHz.
	env := math.Abs(x)
	if env > a.env {
		a.env = env
	} else {
		a.env += 0.999 * (env - a.env)
	}
	desired := a.target / math.Max(a.env, 1e-4)
	if desired > a.max {
		desired = a.max
	}
	if desired < 0.25 {
		desired = 0.25
	}
	// Attack (gain down) fast ~5 ms, release (gain up) slow ~300 ms.
	k := 0.0007
	if desired < a.gain {
		k = 0.04
	}
	a.gain += k * (desired - a.gain)
	return x * a.gain
}
