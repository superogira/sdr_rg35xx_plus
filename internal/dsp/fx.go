// Audio effects shared by every demod path: the noise-reduction
// adaptive filter and the user low-/high-pass biquads.
package dsp

import "math"

// NoiseReduction is the classic ham-DSP adaptive noise canceller: a
// normalized-LMS predictor that is taught to predict the current
// sample from the DELAYED past. Voice is quasi-periodic over a few ms
// and predictable; broadband noise is not — so the prediction is the
// enhanced signal and the prediction error is the noise. Level 1..9
// blends between raw and enhanced and scales the adaptation rate;
// higher levels reduce more noise but may add artefacts to processed
// voices, exactly like the NR knob on commercial transceivers.
type NoiseReduction struct {
	level int // 0 = off, 1..9
	buf   []float64
	w     []float64 // predictor taps
	i     int       // circular write position
	reset bool
}

const (
	nrDelay = 24 // ~3 ms at 8 kHz: decorrelates noise, keeps pitch
	nrTaps  = 32
	nrBuf   = nrDelay + nrTaps + 2
)

func NewNoiseReduction() *NoiseReduction {
	return &NoiseReduction{buf: make([]float64, nrBuf), w: make([]float64, nrTaps)}
}

// SetLevel applies 0 (off) .. 9.
func (n *NoiseReduction) SetLevel(l int) {
	if l < 0 {
		l = 0
	}
	if l > 9 {
		l = 9
	}
	if l != n.level {
		// Taps learned under one setting still predict fine, but the
		// blend changes — a fresh start avoids a lumpy transition.
		for i := range n.w {
			n.w[i] = 0
		}
	}
	n.level = l
}

func (n *NoiseReduction) Level() int { return n.level }

// Step filters one audio sample.
func (n *NoiseReduction) Step(x float64) float64 {
	if n.level == 0 {
		return x
	}
	n.buf[n.i] = x

	// Prediction from the window delayed by nrDelay samples.
	var y, norm float64
	for k := 0; k < nrTaps; k++ {
		s := n.buf[(n.i-nrDelay-k+nrBuf)%nrBuf]
		y += n.w[k] * s
		norm += s * s
	}
	e := x - y

	// Normalized LMS: adaptation speed μ grows with the level; the
	// 1/norm normalisation keeps it stable regardless of loudness.
	// Large μ tracks speech quickly but its misadjustment leaves noise
	// in the prediction, so the curve stays modest.
	mu := 0.03 + 0.03*float64(n.level)
	if norm > 1e-9 {
		g := mu * e / norm
		for k := 0; k < nrTaps; k++ {
			n.w[k] += g * n.buf[(n.i-nrDelay-k+nrBuf)%nrBuf]
		}
	}

	n.i = (n.i + 1) % nrBuf

	// Blend: higher level = more of the enhanced prediction.
	mix := 0.25 + 0.083*float64(n.level) // 1→0.33 … 9→1.0
	return x + mix*(y-x)
}

// biquad is a standard RBJ audio biquad.
type biquad struct {
	on                     bool
	fcHz, fs               float64
	b0, b1, b2, a1, a2     float64
	x1, x2, y1, y2         float64
}

func (b *biquad) setHighpass(fc, fs float64) {
	b.on, b.fcHz, b.fs = true, fc, fs
	fc = clampFc(fc, fs)
	w0 := 2 * math.Pi * fc / fs
	cw := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * 0.707) // Butterworth Q
	a0r := 1 / (1 + alpha)
	b.b0 = (1 + cw) / 2 * a0r
	b.b1 = -(1 + cw) * a0r
	b.b2 = b.b0
	b.a1 = -2 * cw * a0r
	b.a2 = (1 - alpha) * a0r
}

func (b *biquad) setLowpass(fc, fs float64) {
	b.on, b.fcHz, b.fs = true, fc, fs
	fc = clampFc(fc, fs)
	w0 := 2 * math.Pi * fc / fs
	cw := math.Cos(w0)
	alpha := math.Sin(w0) / (2 * 0.707)
	a0r := 1 / (1 + alpha)
	b.b0 = (1 - cw) / 2 * a0r
	b.b1 = (1 - cw) * a0r
	b.b2 = b.b0
	b.a1 = -2 * cw * a0r
	b.a2 = (1 - alpha) * a0r
}

// clampFc keeps the corner inside a sane fraction of Nyquist so the
// cookbook formulas stay stable.
func clampFc(fc, fs float64) float64 {
	if fc < fs/200 {
		fc = fs / 200
	}
	if fc > fs/2*0.95 {
		fc = fs / 2 * 0.95
	}
	return fc
}

func (b *biquad) step(x float64) float64 {
	if !b.on {
		return x
	}
	y := b.b0*x + b.b1*b.x1 + b.b2*b.x2 - b.a1*b.y1 - b.a2*b.y2
	b.x2, b.x1 = b.x1, x
	b.y2, b.y1 = b.y1, y
	return y
}

func (b *biquad) reset() {
	b.x1, b.x2, b.y1, b.y2 = 0, 0, 0, 0
}

// audProc is the per-sample audio post-processing chain attached to
// every demod path: user high-pass → user low-pass → noise reduction.
type audProc struct {
	nr *NoiseReduction
	hp biquad
	lp biquad
}

func newAudProc() *audProc { return &audProc{nr: NewNoiseReduction()} }

// SetNoiseReduction applies 0 (off) .. 9.
func (p *audProc) SetNoiseReduction(level int) { p.nr.SetLevel(level) }

// SetAudioFilters configures the user corner filters (0 Hz = off).
func (p *audProc) SetAudioFilters(hpHz, lpHz int, fs float64) {
	if hpHz > 0 {
		p.hp.setHighpass(float64(hpHz), fs)
	} else {
		p.hp.on = false
	}
	if lpHz > 0 {
		p.lp.setLowpass(float64(lpHz), fs)
	} else {
		p.lp.on = false
	}
}

// step runs one sample through the whole effect chain.
func (p *audProc) step(x float64) float64 {
	x = p.hp.step(x)
	x = p.lp.step(x)
	return p.nr.Step(x)
}
