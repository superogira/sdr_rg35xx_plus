// Package dsp turns a raw rtl_tcp IQ byte stream into audio and spectrum.
//
// Chain (sample rates for IQRate 960000):
//
//	u8 IQ ──float──▶ DC blocker ──▶ complex FIR decim /4 ──▶ 240 ksps IF ─…
//	    …─▶ FM demod (polar discriminator) ─▶ real FIR decim /5 ─▶ 48 ksps …
//	    …─▶ de-emphasis ─▶ scale/clip ─▶ audio out
//
// The 240 ksps IF is also tapped (last 512 complex samples) for the
// waterfall/spectrum display.
package dsp

import "math"

// Rates. The e25wop server accepts ONE SetSampleRate per connection
// (mid-stream changes make its stream unstable), so the app sends the
// chosen rate first thing on connect and dimensions the whole chain for
// it. All rates must divide by 32 (÷8 to the IF, ÷4 to audio). These are
// package variables set via SetIQRate before any Chain is built.
var (
	IQRate    = 2_048_000
	IF2Rate   = IQRate / 8
	AudioRate = IF2Rate / 4
	// DisplaySpanHz is the half-width of spectrum shown — the full IF.
	DisplaySpanHz = IF2Rate / 2
)

// SetIQRate re-dimensions the DSP for a new capture rate. It must run
// BEFORE NewChain (filters are designed from these values).
func SetIQRate(hz int) bool {
	if hz%32_000 != 0 {
		return false
	}
	IQRate = hz
	IF2Rate = hz / 8
	AudioRate = IF2Rate / 4
	DisplaySpanHz = IF2Rate / 2
	return true
}

// Mode bundles the demodulation parameters for one receive mode.
type Mode struct {
	Name      string
	Deviation float64 // peak FM deviation, Hz
	AudioCut  float64 // final low-pass cutoff, Hz
	DeemphTau float64 // seconds; 0 = none
	Squelch   bool    // mute on weak signal (NFM)
}

var (
	// ModeWFM is broadcast FM (200 kHz channel, ±75 kHz deviation).
	ModeWFM = Mode{Name: "WFM", Deviation: 75000, AudioCut: 15000, DeemphTau: 50e-6}
	// ModeNFM is narrowband FM voice (12.5/25 kHz channels, ±2.5 kHz).
	ModeNFM = Mode{Name: "NFM", Deviation: 2500, AudioCut: 2800, DeemphTau: 0, Squelch: true}
)

// Chain holds all per-connection DSP state. Reuse across reconnects is fine
// after Reset.
type Chain struct {
	mode   Mode
	volume float64

	dc DCBlocker

	// IF decimator: complex, IQRate -> IF2Rate.
	ifTaps []float64
	ifHist []complex128
	ifD    int

	demod FMDemod

	// Audio decimator: real, IF2Rate -> AudioRate.
	auTaps []float64
	auHist []float64
	auD    int

	deemph *Deemph

	// Squelch + metering state.
	sqlOpen  bool
	sqlFloor float64 // slowly tracked noise floor, dBFS
	sqlLevel float64 // open threshold above floor, dB
	powerDb  float64 // smoothed IF power, dBFS
	sqlRamp  float64 // output gain ramp 0..1
	muted    bool    // external mute (disconnected)

	// Scratch buffers, reused across Process calls.
	fiq      []complex128
	fif2     []complex128
	fdem     []float64
	audioBuf []float64

	tap    *SpectrumTap // IF-rate tap (after decimation) for narrow spans
	rawTap *SpectrumTap // full-rate tap (before decimation) for wide spans
}

// NewChain builds a chain for the mode with filters designed for the fixed
// global rates. volume defaults to 1.
func NewChain(mode Mode, tap, rawTap *SpectrumTap) *Chain {
	c := &Chain{mode: mode, tap: tap, rawTap: rawTap, volume: 1}
	c.dc = NewDCBlocker(float64(IQRate))
	// 255 taps with cutoff at 0.40×IF2: the stopband lands almost exactly
	// at the IF2 Nyquist edge for every supported rate (the tap count is
	// rate-independent because IQRate/IF2Rate is fixed at 8).
	c.ifTaps = DesignLowpass(255, float64(IF2Rate)*0.40, float64(float64(IQRate)))
	c.ifD = IQRate / IF2Rate
	c.auTaps = DesignLowpass(tapsFor(mode.AudioCut), mode.AudioCut, float64(IF2Rate))
	c.auD = IF2Rate / AudioRate
	if mode.DeemphTau > 0 {
		c.deemph = NewDeemph(mode.DeemphTau, float64(AudioRate))
	}
	c.sqlLevel = 8 // dB above floor
	c.powerDb = -100
	c.sqlFloor = -100
	return c
}

func tapsFor(cutoff float64) int {
	// Narrower cutoffs need longer filters to keep the transition out of
	// the adjacent channel (transition ≈ 3.3/taps × IF2Rate).
	if cutoff >= 10000 {
		return 95 // ~9 kHz transition at 256 ksps
	}
	return 191 // ~4.4 kHz transition at 256 ksps
}

// SetMute ramps audio to silence (used on disconnect) without touching
// filter state.
func (c *Chain) SetMute(m bool) { c.muted = m }

// PowerDb returns the smoothed IF power in dBFS.
func (c *Chain) PowerDb() float64 { return c.powerDb }

// SquelchOpen reports whether the squelch is currently open.
func (c *Chain) SquelchOpen() bool { return c.sqlOpen }

// SetSquelchDb sets the squelch open threshold in dB above the tracked
// noise floor (NFM only). 40 or more means "squelch disabled".
func (c *Chain) SetSquelchDb(db float64) { c.sqlLevel = db }

// SetVolume sets software volume 0..1.5.
func (c *Chain) SetVolume(v float64) {
	if v < 0 {
		v = 0
	}
	if v > 1.5 {
		v = 1.5
	}
	c.volume = v
}

func (c *Chain) Volume() float64 { return c.volume }

// Process consumes raw interleaved uint8 IQ bytes and appends mono audio
// samples (nominally in [-1,1]) to out.
func (c *Chain) Process(iq []byte, out *[]float32) {
	n := len(iq) / 2
	c.fiq = growComplex(c.fiq, n)
	for i := 0; i < n; i++ {
		re := (float64(iq[2*i]) - 127.5) / 127.5
		im := (float64(iq[2*i+1]) - 127.5) / 127.5
		c.fiq[i] = c.dc.Step(complex(re, im))
	}

	if c.rawTap != nil {
		c.rawTap.Push(c.fiq)
	}

	// IF decimation IQRate -> IF2Rate.
	c.fif2 = c.fif2[:0]
	complexFIRDecim(c.ifTaps, &c.ifHist, c.ifD, c.fiq, &c.fif2)

	// Power meter + squelch decision run on the decimated IF.
	c.measure(c.fif2)

	if c.tap != nil {
		c.tap.Push(c.fif2)
	}

	// FM demodulation -> instantaneous frequency in Hz.
	dn := len(c.fif2)
	c.fdem = growFloat(c.fdem, dn)
	for i, z := range c.fif2 {
		c.fdem[i] = c.demod.Step(z)
	}

	// Audio decimation IF2Rate -> AudioRate.
	audio := c.audioBuf[:0]
	realFIRDecim(c.auTaps, &c.auHist, c.auD, c.fdem, &audio)
	c.audioBuf = audio

	scale := 0.85 / c.mode.Deviation // demod outputs Hz; map deviation to ~0.85 FS
	for _, v := range audio {
		x := v * scale
		if c.deemph != nil {
			x = c.deemph.Step(x)
		}
		x = c.applySquelchRamp(x)
		x *= c.volume
		if x > 0.98 {
			x = 0.98
		} else if x < -0.98 {
			x = -0.98
		}
		*out = append(*out, float32(x))
	}
}

// measure updates the power meter and squelch state from one IF block.
func (c *Chain) measure(block []complex128) {
	if len(block) == 0 {
		return
	}
	var p float64
	for _, z := range block {
		p += real(z)*real(z) + imag(z)*imag(z)
	}
	p /= float64(len(block))
	db := 10 * math.Log10(p+1e-12)
	// Fast attack, slow release on the display meter.
	a := 0.25
	if db < c.powerDb {
		a = 0.05
	}
	c.powerDb += a * (db - c.powerDb)

	if !c.mode.Squelch || c.sqlLevel >= 40 {
		c.sqlOpen = true
		return
	}
	// Noise-floor tracking runs ONLY while the squelch is closed (then
	// only noise should be present): converge down fast, up moderately,
	// so it settles on the true noise within ~1 s. While open the floor
	// is frozen — a carrier held for minutes must not walk the floor up
	// and chop the tail of a long transmission. Tuning into an
	// already-busy channel therefore starts closed; open it manually
	// with the squelch cycle (SQL OFF = monitor).
	if !c.sqlOpen {
		if c.sqlFloor < -99 {
			c.sqlFloor = db
		} else if db < c.sqlFloor {
			c.sqlFloor += 0.3 * (db - c.sqlFloor)
		} else {
			c.sqlFloor += 0.05 * (db - c.sqlFloor)
		}
		if c.sqlFloor < -120 {
			c.sqlFloor = -120
		}
		if c.powerDb > c.sqlFloor+c.sqlLevel {
			c.sqlOpen = true
		}
	} else if c.powerDb < c.sqlFloor+c.sqlLevel-6 { // hysteresis
		c.sqlOpen = false
	}
}

// applySquelchRamp ramps the output gain for squelch/mute transitions so
// they never click (5 ms at 48 kHz).
func (c *Chain) applySquelchRamp(x float64) float64 {
	target := 1.0
	if c.muted || (c.mode.Squelch && !c.sqlOpen) {
		target = 0
	}
	step := 200.0 / float64(AudioRate)
	switch {
	case c.sqlRamp < target:
		c.sqlRamp += step
		if c.sqlRamp > target {
			c.sqlRamp = target
		}
	case c.sqlRamp > target:
		c.sqlRamp -= step
		if c.sqlRamp < target {
			c.sqlRamp = target
		}
	}
	return x * c.sqlRamp
}

// Reset clears demod/ramp state for a fresh stream (filters keep history;
// a one-block discontinuity is inaudible after the ramp).
func (c *Chain) Reset() {
	c.demod = FMDemod{}
	c.sqlOpen = false
	c.sqlRamp = 0
}

func growComplex(s []complex128, n int) []complex128 {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]complex128, n)
}

func growFloat(s []float64, n int) []float64 {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]float64, n)
}
