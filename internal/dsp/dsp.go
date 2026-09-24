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
	// 512 kHz is the floor: the NFM path decimates IF2 by 8 to the
	// demod rate, then audio ÷ N — at 512k the NFM demod lands exactly
	// on the 8 kHz audio output. Below that the audio decimation
	// factor truncates to zero and the process dies.
	if hz < 256_000 || hz%32_000 != 0 {
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
	Deviation float64 // peak FM deviation, Hz (FM modes)
	AudioCut  float64 // final low-pass cutoff, Hz (FM modes)
	DeemphTau float64 // seconds; 0 = none
	Squelch   bool    // mute on weak signal (NFM)

	// SSB/CW modes (phasing via complex bandpass at 8 kHz).
	SSB      bool    // use the SSB branch instead of the FM discriminator
	ShiftHz  float64 // band center offset: USB +1500, LSB -1500, CW +700
	HalfBwHz float64 // half of the audio passband: 1300 SSB, 150 CW

	// BwHz is the receive channel bandwidth in Hz (NFM/WFM: the IF
	// channel filter; SSB/CW: the audio passband width). 0 = default.
	BwHz float64
}

// Bandwidths lists the selectable channel bandwidths (Hz) for a mode.
func (m Mode) Bandwidths() []float64 {
	switch {
	case m.Name == "NFM":
		return []float64{5000, 6250, 8000, 10000, 12500, 15000, 20000, 25000}
	case m.Name == "WFM":
		return []float64{50000, 75000, 100000, 150000, 200000, 250000}
	case m.SSB && m.Name != "CW":
		return []float64{500, 1000, 1500, 2000, 2500, 3000, 5000, 6000}
	case m.Name == "CW":
		return []float64{50, 100, 150, 200, 250, 300, 400, 500}
	}
	return nil
}

func (m Mode) String() string { return m.Name }

var (
	// ModeWFM is broadcast FM (200 kHz channel, ±75 kHz deviation).
	ModeWFM = Mode{Name: "WFM", Deviation: 75000, AudioCut: 15000, DeemphTau: 50e-6, BwHz: 200000}
	// ModeNFM is narrowband FM voice (12.5/25 kHz channels, ±2.5 kHz).
	ModeNFM = Mode{Name: "NFM", Deviation: 2500, AudioCut: 2800, DeemphTau: 0, Squelch: true, BwHz: 12500}

	// SSB voice: 200-2800 Hz passband selected by sideband.
	ModeUSB = Mode{Name: "USB", SSB: true, ShiftHz: 1500, HalfBwHz: 1300, BwHz: 2600}
	ModeLSB = Mode{Name: "LSB", SSB: true, ShiftHz: -1500, HalfBwHz: 1300, BwHz: 2600}
	// CW: a 300 Hz window centred on a +700 Hz beat note.
	ModeCW = Mode{Name: "CW", SSB: true, ShiftHz: 700, HalfBwHz: 150, BwHz: 300}
)

// SSBRate is the audio rate of the SSB/CW branch.
const SSBRate = 8000

// ModeList is the cycling order for the mode button/menu.
var ModeList = []Mode{ModeNFM, ModeWFM, ModeUSB, ModeLSB, ModeCW}

// NextMode returns the mode after m in the cycling order.
func NextMode(m Mode) Mode {
	for i, v := range ModeList {
		if v.Name == m.Name {
			return ModeList[(i+1)%len(ModeList)]
		}
	}
	return ModeNFM
}

// AudioOutRate is the audio sample rate this mode's chain produces
// (used to re-configure the output resampler): 8 kHz for NFM/SSB/CW
// (16 kHz for the wide 5/6 kHz SSB settings), 32 kHz for WFM.
func (m Mode) AudioOutRate() int {
	if m.SSB {
		if m.BwHz > 3400 {
			return 2 * SSBRate
		}
		return SSBRate
	}
	if m.Name == "NFM" {
		return SSBRate
	}
	return IF2Rate / 8
}

// OutRate returns the running chain's audio rate (matches
// Mode.AudioOutRate).
func (c *Chain) OutRate() int {
	if c.outRate == 0 {
		return AudioRate
	}
	return c.outRate
}

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

	// Channel filter (selectivity) after the IF decimation. NFM: complex
	// FIR decimates 256k -> 32k with cutoff BwHz/2 (sharp, 511 taps).
	// WFM: no decimation, 127 taps. chRate is the demod input rate.
	chTaps    []float64
	chHist    []complex128
	chD       int
	chRate    int
	chScratch []complex128

	// Audio output rate for this chain (8k NFM/SSB/CW, 32k WFM).
	outRate int

	demod FMDemod

	// SSB branch: decimate 256k -> 8k (wide complex LPF), then a complex
	// bandpass picks the sideband; audio is the real part.
	ssbDecTaps []float64
	ssbDecHist []complex128
	ssbDecD    int
	ssbTaps    []complex128 // rotated lowpass = complex bandpass
	ssbHist    []complex128
	ssbScratch []complex128
	sbOut      []complex128

	// Audio decimator: real, IF2Rate -> AudioRate.
	auTaps []float64
	auHist []float64
	auD    int

	deemph *Deemph
	agc    *AGC         // SSB/CW loudness
	ft8    *FT8Detector // nil = disabled
	agcOn  bool         // AGC enable switch (menu)

	// Passband tuning: rotate the IF by -offsetHz so the LISTENING
	// frequency lands at DC for the demodulator while the LO (and the
	// waterfall) stay put. ncoPhase advances 2π·offsetHz/IF2Rate per
	// IF2 sample and is kept in [0, 2π).
	offsetHz float64
	ncoPhase float64

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
	bw := mode.BwHz
	if bw <= 0 {
		bw = 12500
	}
	switch {
	case mode.SSB:
		// Wide-bandwidth SSB (5/6 kHz options) needs a 16 kHz audio
		// branch — 8 kHz cannot carry more than ±4 kHz. FT8 stays on
		// the 8 kHz branches (its detector assumes that rate).
		c.outRate = SSBRate
		if bw > 3400 {
			c.outRate = 2 * SSBRate
		}
		// Wide lowpass covering the audio band, decimate to outRate.
		decCutoff := float64(c.outRate)/2 - 400
		c.ssbDecTaps = DesignLowpass(255, decCutoff, float64(IF2Rate))
		c.ssbDecD = IF2Rate / c.outRate
		// Sideband bandpass: a lowpass prototype rotated to the band
		// centre. Narrow settings need longer filters; the transition is
		// kept to about half the passband.
		half := bw / 2
		if half < 25 {
			half = 25
		}
		// Shift keeps the low edge at ~200 Hz for wide settings (the
		// fixed per-mode ShiftHz only fits the narrow defaults) —
		// WITHOUT flipping the sideband sign (a plain max() here
		// turned LSB into USB).
		shift := mode.ShiftHz
		if shift >= 0 {
			if half+200 > shift {
				shift = half + 200
			}
		} else if -(half + 200) < shift {
			shift = -(half + 200)
		}
		taps := int(3.3 * float64(c.outRate) / half)
		if taps < 127 {
			taps = 127
		}
		if taps > 2047 {
			taps = 2047
		}
		lp := DesignLowpass(taps, half, float64(c.outRate))
		c.ssbTaps = make([]complex128, len(lp))
		for n, h := range lp {
			ang := 2 * math.Pi * shift * float64(n) / float64(c.outRate)
			c.ssbTaps[n] = complex(h*math.Cos(ang), h*math.Sin(ang))
		}
		c.mode.HalfBwHz = half

	case mode.Name == "NFM":
		// Sharp channel filter that also decimates to 32 kHz; the FM
		// discriminator then runs at 32k and audio lands at 8 kHz.
		c.outRate = SSBRate // 8 kHz
		c.chTaps = DesignLowpass(511, bw/2, float64(IF2Rate))
		c.chD = 8
		c.chRate = IF2Rate / 8
		c.demod = FMDemod{rate: float64(c.chRate)}
		c.auTaps = DesignLowpass(255, mode.AudioCut, float64(c.chRate))
		c.auD = c.chRate / c.outRate

	default: // WFM
		c.outRate = IF2Rate / 8 // 32 kHz
		c.chTaps = DesignLowpass(127, bw/2, float64(IF2Rate))
		c.chD = 1
		c.chRate = IF2Rate
		c.demod = FMDemod{rate: float64(IF2Rate)}
		c.auTaps = DesignLowpass(255, mode.AudioCut, float64(IF2Rate))
		c.auD = IF2Rate / c.outRate
	}
	if mode.DeemphTau > 0 {
		c.deemph = NewDeemph(mode.DeemphTau, float64(AudioRate))
	}
	if mode.SSB {
		c.agc = NewAGC()
		c.agcOn = true
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

// SetFT8Detector attaches or detaches an FT8 detector.
func (c *Chain) SetFT8Detector(d *FT8Detector) {
	c.ft8 = d
}

// SetAGCEnabled toggles the SSB/CW AGC live.
func (c *Chain) SetAGCEnabled(on bool) {
	c.agcOn = on
	if on && c.agc == nil {
		c.agc = NewAGC()
	}
}

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

	// SSB/CW keep the IF-band power meter (their squelch is open
	// anyway); FM modes measure in-band audio power after demod.
	if c.mode.SSB {
		c.measureIF(c.fif2)
	}

	if c.tap != nil {
		c.tap.Push(c.fif2)
	}

	// Passband tuning: rotate the demod path (NOT the taps above —
	// the waterfall shows the raw spectrum around the LO) so the
	// listening frequency lands at DC for every demodulator.
	if c.offsetHz != 0 {
		incr := 2 * math.Pi * c.offsetHz / float64(IF2Rate)
		for i, z := range c.fif2 {
			w := -c.ncoPhase // rotate by -offset
			c.fif2[i] = z * complex(math.Cos(w), math.Sin(w))
			c.ncoPhase += incr
			if c.ncoPhase >= 2*math.Pi {
				c.ncoPhase -= 2 * math.Pi
			} else if c.ncoPhase < 0 {
				c.ncoPhase += 2 * math.Pi
			}
		}
	}

	if c.mode.SSB {
		c.processSSB(out)
		return
	}

	// Channel filter (selectivity) — decimates for NFM, straight for WFM.
	chanf := c.chScratch[:0]
	complexFIRDecim(c.chTaps, &c.chHist, c.chD, c.fif2, &chanf)
	c.chScratch = chanf

	// FM demodulation -> instantaneous frequency in Hz.
	dn := len(chanf)
	c.fdem = growFloat(c.fdem, dn)
	for i, z := range chanf {
		c.fdem[i] = c.demod.Step(z)
	}

	// Audio decimation to the chain output rate.
	audio := c.audioBuf[:0]
	realFIRDecim(c.auTaps, &c.auHist, c.auD, c.fdem, &audio)
	c.audioBuf = audio

	// Squelch decision on CHANNEL power (the complex stream after the
	// channel filter): a strong carrier elsewhere in the ±128 kHz window
	// is filtered out, so only energy inside the selected bandwidth moves
	// the meter — the bug that made the SQL setting feel dead was
	// measuring the whole IF instead.
	c.measureIF(chanf)

	scale := 0.85 / c.mode.Deviation // demod outputs Hz; map deviation to ~0.85 FS
	for _, v := range audio {
		x := v * scale
		if c.deemph != nil {
			x = c.deemph.Step(x)
		}
		x = c.applySquelchRamp(x)
		x *= c.volume
		appendOutput(out, x)
	}
}

// processSSB: decimate the IF to SSBRate, select the sideband with a
// complex bandpass, and emit the real part as audio (×3 gain makes SSB
// levels comparable to the FM path).
func (c *Chain) processSSB(out *[]float32) {
	slow := c.ssbScratch[:0]
	complexFIRDecim(c.ssbDecTaps, &c.ssbDecHist, c.ssbDecD, c.fif2, &slow)
	c.ssbScratch = slow

	side := c.sbOut[:0]
	complexCIFIR(c.ssbTaps, &c.ssbHist, slow, &side)
	c.sbOut = side

	// FT8 expects 8 kHz audio — the wide 16 kHz SSB branches cannot
	// feed it (the detector's whole timing assumes 8 k).
	if c.ft8 != nil && c.outRate == SSBRate {
		ft8buf := make([]float64, 0, len(side))
		for _, z := range side {
			ft8buf = append(ft8buf, real(z)*3.0)
		}
		c.ft8.Feed(ft8buf)
	}
	for _, z := range side {
		x := real(z) * 3.0
		if c.agc != nil && c.agcOn {
			x = c.agc.Step(x)
		}
		x = c.applySquelchRamp(x)
		x *= c.volume
		appendOutput(out, x)
	}
}

// appendOutput clamps and appends one audio sample.
func appendOutput(out *[]float32, x float64) {
	if x > 0.98 {
		x = 0.98
	} else if x < -0.98 {
		x = -0.98
	}
	*out = append(*out, float32(x))
}

// measure updates the power meter and squelch state from one IF block.
func (c *Chain) measureIF(block []complex128) {
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

// SetOffsetHz sets the passband tuning offset (listening freq − LO).
// Zero (the default) demodulates at the LO exactly as before.
func (c *Chain) SetOffsetHz(hz float64) {
	c.offsetHz = hz
	c.ncoPhase = 0
}

// OffsetHz returns the passband tuning offset.
func (c *Chain) OffsetHz() float64 { return c.offsetHz }
