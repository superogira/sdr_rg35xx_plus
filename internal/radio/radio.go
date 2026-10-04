// Package radio owns the rtl_tcp connection lifecycle: connect, configure
// the dongle, stream IQ through the DSP into the audio pipe, reconnect with
// backoff, and apply live parameter changes (frequency/mode/gain).
package radio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"sync"
	"time"

	"sdr35/internal/ais"
	"sdr35/internal/audio"
	"sdr35/internal/dsp"
	"sdr35/internal/i18n"
	"sdr35/internal/rtltcp"
)

// r828dGains is the RTL-SDR Blog V4 (R828D) gain table: index = rtl_tcp
// gain index, value = dB. The user-facing gain is dB (ham convention, and
// what the dongle's own tooling prints); the protocol takes the index.
var r828dGains = []float64{
	0.0, 0.9, 1.4, 2.7, 3.7, 7.7, 8.7, 12.5, 14.4, 15.7, 16.6, 19.7, 20.7,
	22.9, 25.4, 28.0, 29.7, 32.8, 33.8, 36.4, 37.2, 38.6, 40.2, 42.1, 43.4,
	43.9, 44.5, 48.0, 49.6,
}

// GainDbToIndex maps a dB setting onto the nearest table index.
func GainDbToIndex(db float64) int {
	best, bestDiff := 0, math.MaxFloat64
	for i, g := range r828dGains {
		if d := math.Abs(g - db); d < bestDiff {
			best, bestDiff = i, d
		}
	}
	return best
}

// GainIndexDb is the dB value of a table index.
func GainIndexDb(idx int) float64 {
	if idx < 0 || idx >= len(r828dGains) {
		return 0
	}
	return r828dGains[idx]
}

const (
	readBufBytes = 64 * 1024
	readTimeout  = 6 * time.Second // Wi-Fi stall → reconnect, not silence
	// flatLimit ≈ 3 s of constant filler (64 KB ≈ 16 ms) before we call
	// the stream dead.
	flatLimit = 190
)

// Radio is safe for concurrent use: the UI thread calls the setters and
// Snapshot while the Run goroutine streams.
type Radio struct {
	Host string
	demo bool

	tap    *dsp.SpectrumTap
	rawTap *dsp.SpectrumTap // full-rate tap for wide waterfall spans
	out    *audio.Output    // nil = waterfall only
	name   string           // audio backend name for status

	mu         sync.Mutex
	freqHz     int64
	loHz       int64
	mode       dsp.Mode
	gainDb     float64 // tuner gain in dB at connect; negative = AGC
	vol        float64
	sqlDb      float64 // NFM squelch threshold, absolute dBFS (>=0 = off)
	nrLevel    int     // audio noise reduction 0..9
	rttyOn     bool
	rtty       *dsp.RTTYDecoder
	wefaxOn    bool
	wefax      *dsp.WefaxDecoder
	cwOn       bool
	cw         *dsp.CWDecoder
	hpHz       int  // user audio high-pass corner, 0 = off
	lpHz       int  // user audio low-pass corner, 0 = off
	iqRate     int  // capture sample rate in Hz (server default 2.048M)
	dsMode     int  // -1 auto (DS below 24 MHz), 0 force off, 2 force on (Q)
	agcOn      bool // SSB/CW AGC enabled
	ft8On      bool
	ft8        *dsp.FT8Detector
	aisRFOn    bool
	audioTap   func(mono []float32, rate int) // web audio stream tap
	localMute  bool                           // speaker off; web tap still live
	aisA, aisB *ais.ChannelDemod
	aisPay     func(payload []byte, ch int, levelDb float64)
	hfApplied  int  // direct-sampling mode currently set on the server
	ppm        int  // tuner frequency correction, applied live and at every (re)connect
	ppmOff     bool // true = leave the correction to the server's own setting

	client  *rtltcp.Client
	gains   int32
	chain   *dsp.Chain
	state   int // 0 disconnected, 1 connecting, 2 streaming
	lastErr string
	reconAt time.Time
	bytesRx uint64

	usbSrc  bool        // host == "usb": feed from the local dongle
	usbProc *exec.Cmd   // our spawned rtl_tcp (Linux sidecar)
	usbMu   sync.Mutex  // guards usbProc across ensure/stop
}

const (
	stateDisconnected = iota
	stateConnecting
	stateStreaming
)

// New builds a radio. gainDb is the tuner gain in dB at connect (negative
// = AGC); it is sent as tenths-of-dB (the protocol path that needs no gain
// table index at all — same recipe the user's rtl-sdr-web-monitor uses).
func New(host string, freqHz int64, mode dsp.Mode, gainDb float64, out *audio.Output) *Radio {
	// Sanitize the startup frequency (a hand-edited or corrupted config
	// must not reach the dongle).
	if freqHz < 500_000 {
		freqHz = 500_000 // RTL-SDR Blog V4 lower edge (HF direct sampling)
	}
	if freqHz > 1_766_000_000 {
		freqHz = 1_766_000_000
	}
	if gainDb > 49.6 {
		gainDb = 49.6
	}
	rttyDec := dsp.NewRTTYDecoder()
	r := &Radio{
		rtty:   rttyDec,
		wefax:  dsp.NewWefaxDecoder(),
		cw:     dsp.NewCWDecoder(),
		Host:   host,
		tap:    dsp.NewSpectrumTap(),
		rawTap: dsp.NewSpectrumTapN(dsp.RawTapLen),
		out:    out,
		freqHz: freqHz,
		loHz:   freqHz,
		mode:   mode,
		gainDb: gainDb,
		vol:    1,
		sqlDb:  -40,
		iqRate: 2_048_000,
		agcOn:  true,
		usbSrc: host == USBHost,
		ft8:    dsp.NewFT8Detector(),
		chain:  dsp.NewChain(mode, nil, nil),
	}
	r.aisA = ais.NewChannelDemod(48000, 0, "A", func(p []byte, ch int, levelDb float64) {
		if f := r.aisPay; f != nil {
			f(p, ch, levelDb)
		}
	})
	r.aisB = ais.NewChannelDemod(48000, 1, "B", func(p []byte, ch int, levelDb float64) {
		if f := r.aisPay; f != nil {
			f(p, ch, levelDb)
		}
	})
	return r
}

func (r *Radio) Tap() *dsp.SpectrumTap    { return r.tap }
func (r *Radio) RawTap() *dsp.SpectrumTap { return r.rawTap }

// IQRate returns the configured capture rate.
func (r *Radio) IQRate() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.iqRate
}

// SetCaptureRate switches the capture sample rate (supported: 2.048M and
// 1.024M — both verified against the server; 512 kHz was never a real
// server rate and made the stream run at an unsupported speed, heard as
// time-stretched audio). The whole DSP chain is
// re-dimensioned and the client dropped, so the next session opens with
// a SetSampleRate as its very first command — the only rate-change
// pattern the server tolerates (mid-stream changes destabilize it).
func (r *Radio) SetCaptureRate(hz int) {
	// Rates from the RTL-SDR hardware (see the server's own list);
	// our DSP needs IQRate divisible by 64 kHz (SSB: /8→/8 to 8 kHz).
	// Lower rates halve the network load — useful on mobile hotspots.
	if hz != 256_000 && hz != 1_024_000 && hz != 1_536_000 &&
		hz != 1_792_000 && hz != 2_048_000 && hz != 2_560_000 &&
		hz != 2_880_000 && hz != 3_200_000 {
		return
	}
	r.mu.Lock()
	if r.iqRate == hz {
		r.mu.Unlock()
		return
	}
	r.iqRate = hz
	r.mu.Unlock()
	if !dsp.SetIQRate(hz) {
		return
	}
	r.mu.Lock()
	r.chain = dsp.NewChain(r.mode, r.tap, r.rawTap)
	r.chain.SetVolume(r.vol)
	r.chain.SetSquelchDb(r.sqlDb)
	r.chain.SetAGCEnabled(r.agcOn)
	r.applyAudioFx()
	if r.ft8On {
		r.chain.SetFT8Detector(r.ft8)
	}
	client := r.client
	r.mu.Unlock()
	if r.out != nil {
		// The chain's audio rate changed with the capture rate —
		// re-tune the resampler to the MODE's rate (dsp.AudioRate is
		// the old fixed 32k constant, wrong at 1.024M/512k).
		r.out.SetInputRate(r.mode.AudioOutRate())
	}
	if client != nil {
		client.CloseGraceful() // reconnect with the new rate
	}
	fmt.Fprintf(os.Stderr, "radio: capture rate now %d Hz (IF2 %d, audio %d)\n", hz, dsp.IF2Rate, dsp.AudioRate)
}

// NewDemo builds a Radio whose Run generates a synthetic signal in-process
// instead of connecting anywhere (display/audio development without a
// dongle).
func NewDemo(mode dsp.Mode, out *audio.Output) *Radio {
	r := New(i18n.T("demo_host"), 145_500_000, mode, -1, out)
	r.demo = true
	return r
}

// Run is the connection loop; it returns when ctx is done.
func (r *Radio) Run(ctx context.Context) {
	// Leaving the app (or dropping to another source) must release the
	// dongle for other programs.
	defer r.stopUSBSrv()
	if r.demo {
		r.mu.Lock()
		r.state = stateStreaming
		r.chain = dsp.NewChain(r.mode, r.tap, r.rawTap)
		r.chain.SetVolume(r.vol)
		r.mu.Unlock()
		dsp.RunDemo(ctx, func() *dsp.Chain {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.chain
		}, func(audio []float32) {
			r.mu.Lock()
			tap := r.audioTap
			rate := r.mode.AudioOutRate()
			muted := r.localMute
			r.mu.Unlock()
			if r.out != nil && !muted {
				r.out.WriteAudio(audio)
			}
			if tap != nil && len(audio) > 0 {
				tap(audio, rate)
			}
		})
		return
	}
	// Reconnect pacing tuned to this server's restart cycle: every client
	// departure kills the rtl_tcp process and the .bat wrapper takes
	// several seconds to bring it back ("all threads dead.. → listening"),
	// so retrying after 1 s just hammers the dead window and can re-trigger
	// the exit cycle. Start at 5 s, ramp to 15 s, jitter ±20%.
	backoff := 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		disabled := r.Host == ""
		r.mu.Unlock()
		if disabled {
			// Radio feed off (host list "(ปิดใช้งาน)"): no dialing, no
			// error state — just idle until a host is picked again.
			r.mu.Lock()
			r.state = stateDisconnected
			r.lastErr = ""
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			backoff = 5 * time.Second
			continue
		}
		if err := r.session(ctx); err != nil {
			wait := backoff + time.Duration(rand.Int63n(int64(backoff)/5))
			r.mu.Lock()
			r.state = stateDisconnected
			r.lastErr = err.Error()
			r.reconAt = time.Now().Add(wait)
			r.mu.Unlock()
			fmt.Fprintf(os.Stderr, "radio: session ended (%v) — retrying in %s\n", err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if backoff < 15*time.Second {
				backoff += 5 * time.Second
			}
			// Feed silence to the player while waiting so it never
			// underruns audibly.
			if r.out != nil {
				r.out.Silence(int(wait.Milliseconds()))
			}
			continue
		}
		backoff = 5 * time.Second
	}
}

// session runs one connect/stream cycle. Returns nil only on ctx done.
func (r *Radio) session(ctx context.Context) error {
	r.mu.Lock()
	r.state = stateConnecting
	r.lastErr = ""
	dialAddr := r.Host
	usbSrc := r.usbSrc
	r.mu.Unlock()

	if usbSrc {
		if err := r.ensureUSBSrv(); err != nil {
			return fmt.Errorf("usb: %v", err)
		}
		dialAddr = usbDialAddr
	}

	client, err := rtltcp.Dial(dialAddr, 5*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "radio: connect to %s failed: %v\n", dialAddr, err)
		return fmt.Errorf("connect: %v", err)
	}
	fmt.Fprintf(os.Stderr, "radio: connected to %s (tuner %d, %d gains, bigEndian=%v)\n",
		dialAddr, client.Info.TunerType, client.Info.GainCount, client.BigEndian())

	setup := func() error {
		r.mu.Lock()
		r.client = client
		r.gains = client.Info.GainCount
		chain := dsp.NewChain(r.mode, r.tap, r.rawTap)
		chain.SetVolume(r.vol)
		chain.SetSquelchDb(r.sqlDb)
		chain.SetAGCEnabled(r.agcOn)
		r.applyAudioFx()
		if r.ft8On {
			chain.SetFT8Detector(r.ft8)
		}
		if r.aisRFOn {
			chain.SetAISDemods(r.aisA, r.aisB)
		}
		r.chain = chain
		r.state = stateStreaming
		freq, gainDb, lo, ppm, ppmOff := r.freqHz, r.gainDb, r.loHz, r.ppm, r.ppmOff
		r.mu.Unlock()
		chain.Reset()
		// Restore the passband offset on the fresh chain (the server
		// below tunes the LO; the offset itself is pure DSP).
		if off := freq - lo; off != 0 {
			chain.SetOffsetHz(float64(off))
		}

		if r.iqRate != 2_048_000 {
			// The server accepts exactly one rate setting per connection
			// and must see it before anything else.
			if err := client.SetSampleRate(uint32(r.iqRate)); err != nil {
				return fmt.Errorf("sample rate: %w", err)
			}
			fmt.Fprintf(os.Stderr, "radio: requested %d Hz sample rate\n", r.iqRate)
		}
		// Stream kick: this server holds the IQ stream until the first
		// command arrives (a silent connect can sit at zero bytes), and
		// SDRSharp opens with the same freq-correction command — so the
		// kick doubles as applying the user's ppm on every (re)connect
		// (a live change was verified safe against the real server).
		// ppmOff sends nothing: a server with its own calibration must
		// not be overridden, so the initial tune below serves as kick.
		if !ppmOff {
			if err := client.SetFreqCorrection(int32(ppm)); err != nil {
				return fmt.Errorf("freq correction kick: %w", err)
			}
		}
		// Dongle bring-up. Configure once, then only ever retune:
		// this server build (fixed 2.048 Msps, big-endian protocol)
		// wedged into a zero stream after receiving a SetSampleRate
		// mid-session, so the app sends exactly the commands proven
		// safe — frequency (live) and gain (connect-time only). No
		// SetSampleRate at all: the DSP chain is hard-wired to the
		// rate the server actually streams. Gain goes out as tenths of
		// dB via CMD_SET_GAIN — the recipe the user's own
		// rtl-sdr-web-monitor uses against this same server; index
		// based gain is avoided entirely.
		r.mu.Lock()
		want := r.directSamplingFor(lo)
		r.hfApplied = want
		r.mu.Unlock()
		if want != 0 {
			if err := client.SetDirectSampling(want); err != nil {
				return fmt.Errorf("direct sampling: %w", err)
			}
		}
		if err := client.SetFrequency(uint32(lo)); err != nil {
			return fmt.Errorf("initial tune: %w", err)
		}
		if gainDb < 0 {
			if err := client.SetTunerAGC(true); err != nil {
				return fmt.Errorf("tuner agc: %w", err)
			}
			if err := client.SetRTLAGC(true); err != nil {
				return fmt.Errorf("rtl agc: %w", err)
			}
		} else {
			if err := client.SetTunerAGC(false); err != nil {
				return fmt.Errorf("tuner agc off: %w", err)
			}
			if err := client.SetGainTenthsDB(int32(math.Round(gainDb * 10))); err != nil {
				return fmt.Errorf("gain: %w", err)
			}
		}
		fmt.Fprintf(os.Stderr, "radio: connected, tuned %.4f MHz gain %.1fdB\n", float64(freq)/1e6, gainDb)
		return nil
	}
	if err := setup(); err != nil {
		return err
	}

	buf := make([]byte, readBufBytes)
	var audioBuf []float32
	flatBlocks := 0
	// Actual-rate watchdog: this server remembers its last-set rate across
	// connections, so the stream may arrive at a different Msps than the
	// DSP assumes (observed: 1.024M stream into a 2.048M chain → audio
	// plays ~2x fast with constant underruns). Measure the real rate
	// after the initial burst settles and re-dimension the DSP to match.
	var rateT0 time.Time
	var rateB0 uint64
	rateDone := false
	// Cumulative drop monitor: nominal bytes vs received over the whole
	// session. A deficit means the SERVER dropped samples (WiFi jitter
	// on its side) — heard on SSB/CW as a momentary pitch slide.
	var sessT0 time.Time
	var lastDropChk time.Time
	defer func() {
		r.mu.Lock()
		if r.client == client {
			r.client = nil
		}
		r.mu.Unlock()
		// Graceful: FIN + drain leaves the single-client server in its
		// accept state instead of wedged on a reset mid-stream.
		client.CloseGraceful()
		if r.chain != nil {
			r.chain.SetMute(true)
		}
	}()

	for ctx.Err() == nil {
		client.SetReadDeadline(time.Now().Add(readTimeout))
		n, err := client.ReadIQ(buf)
		if n > 0 {
			// Dead-stream detector: a wedged server keeps the TCP flow
			// but sends constant filler bytes. Reconnect instead of
			// showing a silent black waterfall forever.
			if flatData(buf[:n]) {
				flatBlocks++
				if flatBlocks > flatLimit {
					return errors.New(i18n.T("dead_stream"))
				}
			} else {
				flatBlocks = 0
			}
			r.mu.Lock()
			chain := r.chain
			r.mu.Unlock()
			audioBuf = audioBuf[:0]
			chain.Process(buf[:n], &audioBuf)
			r.mu.Lock()
			tap := r.audioTap
			rate := r.mode.AudioOutRate()
			muted := r.localMute
			r.mu.Unlock()
			if r.out != nil && !muted {
				r.out.WriteAudio(audioBuf)
			}
			if tap != nil && len(audioBuf) > 0 {
				tap(audioBuf, rate)
			}
			r.mu.Lock()
			r.bytesRx += uint64(n)
			total := r.bytesRx
			r.mu.Unlock()
			if !rateDone {
				if rateT0.IsZero() && total >= 512*1024 {
					rateT0 = time.Now()
					rateB0 = total
				} else if !rateT0.IsZero() && time.Since(rateT0) >= 6*time.Second {
					actual := float64(total-rateB0) / time.Since(rateT0).Seconds() / 2
					// Snap only to rates the server actually supports.
					// Quantizing one burst measurement to an arbitrary
					// 32 kHz multiple once re-dimensioned the DSP to
					// 576 kHz for a whole session while the stream ran
					// slower — every mode then played time-stretched
					// by the mismatch (FT8 slots audibly >15 s).
					snap := 0
					for _, known := range []int{256_000, 640_000, 1_024_000, 1_536_000, 1_792_000, 2_048_000, 2_560_000, 2_880_000, 3_200_000} {
						if actual >= float64(known)*0.85 && actual <= float64(known)*1.15 {
							snap = known
							break
						}
					}
					if snap == 0 {
						// Matches neither supported rate (server ramp-up
						// or TCP burst skew). Keep the configured chain —
						// connect already requested a supported rate — and
						// never kill the session over one bad measurement.
						fmt.Fprintf(os.Stderr, "radio: measured %.3f Msps matches no supported rate — keeping %d Hz\n", actual/1e6, dsp.IQRate)
					} else if snap != dsp.IQRate {
						fmt.Fprintf(os.Stderr, "radio: stream measures %.3f Msps — re-dimensioning DSP from %d to %d Hz\n", actual/1e6, dsp.IQRate, snap)
						dsp.SetIQRate(snap)
						r.SetMode(r.Mode()) // rebuilds the chain + resampler rate
					} else {
						fmt.Fprintf(os.Stderr, "radio: stream rate confirmed %d Hz (%.3f Msps measured)\n", snap, actual/1e6)
					}
					rateDone = true
					sessT0 = rateT0
					lastDropChk = time.Now()
				}
			}
			if rateDone && time.Since(lastDropChk) >= 10*time.Second {
				lastDropChk = time.Now()
				r.mu.Lock()
				total := r.bytesRx
				r.mu.Unlock()
				elapsed := time.Since(sessT0).Seconds()
				got := int64(total - rateB0)
				// Use the CURRENT DSP rate — the watchdog may have
				// re-dimensioned it after this monitor captured its
				// nominal rate.
				r.mu.Lock()
				nominal := int64(dsp.IQRate) * 2
				r.mu.Unlock()
				expect := int64(float64(nominal) * elapsed)
				deficit := 100 * float64(expect-got) / float64(expect)
				if deficit > 0.5 {
					fmt.Fprintf(os.Stderr, "radio: sample drop detected — %.1f%% short over %.0fs (expect %d got %d bytes)\n", deficit, elapsed, expect, got)
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("stream: %v", err)
		}
	}
	return nil
}

// flatData reports whether the block is constant filler (sampled).
func flatData(b []byte) bool {
	if len(b) < 3 {
		return true
	}
	first := b[0]
	check := func(chunk []byte) bool {
		for _, v := range chunk {
			if v != first {
				return false
			}
		}
		return true
	}
	n := len(b)
	return check(b[:min(256, n)]) && check(b[n/2:n/2+min(256, n-n/2)]) && check(b[max(0, n-256):])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SetFreq tunes immediately when streaming. A failed command write means
// the connection is poisoned (partial frame) — closing the client makes
// the session loop reconnect with a clean stream.
func (r *Radio) SetFreq(hz int64) {
	if hz < 500_000 {
		hz = 500_000 // RTL-SDR Blog V4 lower edge (HF direct sampling)
	}
	if hz > 1_766_000_000 {
		hz = 1_766_000_000
	}
	r.mu.Lock()
	// Passband tuning: while the new listening frequency stays inside
	// the current LO's receive window, move only the DSP offset — no
	// server command, no retune gap, no tune-spam while scrolling.
	off := hz - r.loHz
	if chain := r.chain; chain != nil && r.client != nil && abs64(off) <= r.offsetLimit() {
		r.freqHz = hz
		chain.SetOffsetHz(float64(off))
		r.mu.Unlock()
		return
	}
	// Out of window (or not connected yet): retune the LO onto the
	// listening frequency and zero the offset.
	r.freqHz = hz
	r.loHz = hz
	client := r.client
	want := r.directSamplingFor(hz)
	switchHF := want != r.hfApplied
	r.hfApplied = want
	chain := r.chain
	r.mu.Unlock()
	if chain != nil {
		chain.SetOffsetHz(0)
	}
	if client == nil {
		return
	}
	if switchHF {
		if err := client.SetDirectSampling(want); err != nil {
			fmt.Fprintf(os.Stderr, "radio: direct sampling %d failed: %v\n", want, err)
			client.CloseGraceful()
			return
		}
		fmt.Fprintf(os.Stderr, "radio: direct sampling mode %d (freq %.4f MHz)\n", want, float64(hz)/1e6)
	}
	if err := client.SetFrequency(uint32(hz)); err != nil {
		fmt.Fprintf(os.Stderr, "radio: tune %d failed: %v — reconnecting\n", hz, err)
		client.CloseGraceful()
		return
	}
	// Logged so the app log can be compared against the rtl_tcp server's
	// own console when a tuning glitch is investigated.
	fmt.Fprintf(os.Stderr, "radio: tune %d (%.4f MHz)\n", hz, float64(hz)/1e6)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// offsetLimit is how far the listening frequency may sit from the LO
// before a hardware retune is required: the IF decimation filter
// passes ~±0.4·IF2Rate; keep a mode-bandwidth of margin.
func (r *Radio) offsetLimit() int64 {
	// The NCO rotates at the FULL IQ rate (before IF2 decimation), so
	// the window is bounded by the IQ Nyquist minus the IF2 filter's
	// half-width (the rotated signal must still fit through the
	// decimation filter without aliasing).
	lim := int64(0.45 * float64(dsp.IQRate))
	if bw := int64(r.mode.BwHz); bw*2 < lim {
		lim -= bw
	}
	return lim
}

// LO returns the hardware tuning (waterfall centre).
func (r *Radio) LO() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loHz
}

func (r *Radio) Freq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.freqHz
}

// applyAudioFx (re)applies the user audio-effect settings to a chain.
// Every chain rebuild must run it — SetMode, SetBandwidth, SetCaptureRate
// and session setup all create fresh chains.
func (r *Radio) applyAudioFx() {
	r.chain.SetNoiseReduction(r.nrLevel)
	r.chain.SetAudioFilters(r.hpHz, r.lpHz)
	if r.rttyOn {
		r.chain.SetRTTYDetector(r.rtty)
	}
	if r.wefaxOn {
		r.chain.SetWefaxDecoder(r.wefax)
	} else {
		r.chain.SetWefaxDecoder(nil)
	}
	if r.cwOn {
		r.chain.SetCWDecoder(r.cw)
	} else {
		r.chain.SetCWDecoder(nil)
	}
}

// SetNoiseReduction applies the audio NR level 0 (off) .. 9.
func (r *Radio) SetNoiseReduction(level int) {
	if level < 0 {
		level = 0
	}
	if level > 9 {
		level = 9
	}
	r.mu.Lock()
	r.nrLevel = level
	r.applyAudioFx()
	r.mu.Unlock()
}

// NoiseReduction returns the NR level (0 = off).
func (r *Radio) NoiseReduction() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nrLevel
}

// SetAudioFilter sets one user corner filter: which is "hp" or "lp",
// hz 0 = off. The other corner is kept.
func (r *Radio) SetAudioFilter(which string, hz int) {
	if hz < 0 {
		hz = 0
	}
	if hz > 6000 {
		hz = 6000
	}
	r.mu.Lock()
	if which == "hp" {
		r.hpHz = hz
	} else {
		r.lpHz = hz
	}
	r.applyAudioFx()
	r.mu.Unlock()
}

// AudioFilter returns the current HP and LP corners (0 = off).
func (r *Radio) AudioFilter() (hpHz, lpHz int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hpHz, r.lpHz
}

// SetMode swaps the demod chain. While FT8 is active the mode is locked
// to USB — the FT8 operating convention — so the demod can never sit on
// a mode that fights the monitor branch (and the listener gets the
// conventional audio). A re-set of the SAME mode is allowed (internal
// chain rebuilds depend on it).
// SetMode switches the demod mode. It returns an i18n key
// ("ft8_modelock"/"aisrf_modelock") when the change was rejected
// because FT8/AIS RF holds the mode; callers may ignore the return.
func (r *Radio) SetMode(mode dsp.Mode) string {
	r.mu.Lock()
	if r.ft8On && mode.Name != dsp.ModeUSB.Name && mode.Name != r.mode.Name {
		r.mu.Unlock()
		fmt.Fprintf(os.Stderr, "radio: mode change to %s ignored — FT8 locks USB\n", mode.Name)
		return "ft8_modelock"
	}
	if r.aisRFOn && mode.Name != dsp.ModeNFM.Name && mode.Name != r.mode.Name {
		r.mu.Unlock()
		fmt.Fprintf(os.Stderr, "radio: mode change to %s ignored (AIS RF locks NFM)"+string(rune(10)), mode.Name)
		return "aisrf_modelock"
	}
	r.mode = mode
	r.chain = dsp.NewChain(mode, r.tap, r.rawTap)
	r.chain.SetVolume(r.vol)
	r.chain.SetSquelchDb(r.sqlDb)
	r.chain.SetAGCEnabled(r.agcOn)
	r.applyAudioFx()
	if r.ft8On {
		r.chain.SetFT8Detector(r.ft8)
	}
	if r.aisRFOn {
		r.chain.SetAISDemods(r.aisA, r.aisB)
	}
	// The fresh chain starts at offset 0 — restore the passband offset
	// so switching modes mid-scroll keeps listening where the dial says.
	if off := r.freqHz - r.loHz; off != 0 {
		r.chain.SetOffsetHz(float64(off))
	}
	// Update the resampler INSIDE the lock: leaving it outside let the
	// DSP goroutine write new-rate audio into an old-rate resampler
	// during rapid mode cycling — the corrupted state crashed the app.
	out := r.out
	audioRate := mode.AudioOutRate()
	r.mu.Unlock()
	if out != nil {
		out.SetInputRate(audioRate)
	}
	return ""
}

// CycleSquelch steps the NFM squelch threshold through useful dBFS
// presets and returns the new label for the UI. Pure DSP — no server
// commands.
func (r *Radio) CycleSquelch() string {
	r.mu.Lock()
	var next float64
	switch r.sqlDb {
	case -60:
		next = -50
	case -50:
		next = -40
	case -40:
		next = -30
	case -30:
		next = 0
	default:
		next = -60
	}
	r.sqlDb = next
	r.chain.SetSquelchDb(next)
	r.mu.Unlock()
	return r.SquelchLabel()
}

// SquelchLabel describes the squelch setting.
func (r *Radio) SquelchLabel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sqlDb >= 0 {
		return i18n.T("sql_disabled")
	}
	return fmt.Sprintf(i18n.T("sql_fmt"), r.sqlDb)
}

func (r *Radio) Mode() dsp.Mode {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode
}

// SetVolume stores software volume and applies it to the live chain.
func (r *Radio) SetVolume(v float64) {
	r.mu.Lock()
	r.vol = v
	chain := r.chain
	r.mu.Unlock()
	if chain != nil {
		chain.SetVolume(v)
	}
}

// Volume returns the current software volume.
func (r *Radio) Volume() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.vol
}

// SetHost changes the server address; the next reconnect uses it.
// Ppm returns the tuner frequency correction in ppm.
func (r *Radio) Ppm() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ppm
}

// PpmOff reports whether the app leaves the correction to the server.
func (r *Radio) PpmOff() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ppmOff
}

// SetPpmOff toggles hands-off mode (no SetFreqCorrection is sent, on
// this connection or any later one, until it is turned off again).
func (r *Radio) SetPpmOff(off bool) {
	r.mu.Lock()
	r.ppmOff = off
	r.mu.Unlock()
}

// SetPpm stores the frequency correction and, while streaming, sends it
// straight away (proven safe mid-session against the user's server).
func (r *Radio) SetPpm(v int) {
	if v > 120 {
		v = 120
	}
	if v < -120 {
		v = -120
	}
	r.mu.Lock()
	r.ppm = v
	client := r.client
	r.mu.Unlock()
	if client != nil {
		client.SetFreqCorrection(int32(v))
	}
}

// SetHost retargets the rtl_tcp connection. An empty host disables the
// radio entirely (no IQ, no decoding) — used by the host list's
// "(ปิดใช้งาน)" row when the app is only wanted as an ADS-B/AIS viewer.
// The magic value USBHost ("usb") switches to the local dongle: the app
// spawns its own rtl_tcp and dials 127.0.0.1:1234.
func (r *Radio) SetHost(host string) {
	r.mu.Lock()
	wasUSB := r.usbSrc
	if r.Host == host && wasUSB == (host == USBHost) {
		r.mu.Unlock()
		return
	}
	r.Host = host
	r.usbSrc = host == USBHost
	client := r.client
	r.mu.Unlock()
	if wasUSB && !r.usbSrc {
		r.stopUSBSrv() // release the dongle for other programs
	}
	if client != nil {
		client.CloseGraceful() // reconnect to the new address
	}
	fmt.Fprintf(os.Stderr, "radio: host now %s (reconnecting)\n", host)
}

// SetRTTYEnabled turns the RTTY decoder on/off. No mode lock — the
// monitor branch decodes in every mode (RTTY convention is USB, mark
// tone tuned to +2125 Hz on the waterfall).
func (r *Radio) SetRTTYEnabled(on bool) {
	r.mu.Lock()
	r.rttyOn = on
	r.applyAudioFx()
	r.mu.Unlock()
}

// SetWefaxEnabled turns the HF-FAX decoder on/off (decodes in every
// mode via the monitor branch; convention is USB with the dial 1.9 kHz
// below the station's assigned frequency).
func (r *Radio) SetWefaxEnabled(on bool) {
	r.mu.Lock()
	r.wefaxOn = on
	r.applyAudioFx()
	r.mu.Unlock()
}

// WefaxEnabled reports whether the WEFAX decoder is running.
func (r *Radio) WefaxEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.wefaxOn
}

// SetCWDecodeEnabled turns the Morse decoder on/off. In CW mode the
// beat note sits at +700 Hz, which is exactly where the decoder listens.
func (r *Radio) SetCWDecodeEnabled(on bool) {
	r.mu.Lock()
	r.cwOn = on
	r.applyAudioFx()
	r.mu.Unlock()
}

// CWDecodeEnabled reports whether the Morse decoder is running.
func (r *Radio) CWDecodeEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cwOn
}

// CW exposes the decoder for the UI/log pollers.
func (r *Radio) CW() *dsp.CWDecoder { return r.cw }

// Wefax exposes the decoder for preview/snapshot/save.
func (r *Radio) Wefax() *dsp.WefaxDecoder { return r.wefax }

// SetWefaxAutoSave toggles finishing on the APT stop tone (off =
// receive continuously, saving is manual).
func (r *Radio) SetWefaxAutoSave(on bool) {
	r.mu.Lock()
	r.wefax.SetAutoSave(on)
	r.mu.Unlock()
}

// WefaxAutoSave reports the WEFAX auto-finish mode.
func (r *Radio) WefaxAutoSave() bool {
	return r.wefax.AutoSave()
}

// RTTYEnabled reports whether RTTY decoding is active.
func (r *Radio) RTTYEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rttyOn
}

// SetRTTYReversed swaps mark/space polarity (LSB or reversed-keyed TX).
func (r *Radio) SetRTTYReversed(on bool) {
	r.mu.Lock()
	r.rtty.SetReversed(on)
	r.mu.Unlock()
}

// RTTYReversed reports the mark/space polarity flag.
func (r *Radio) RTTYReversed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rtty.Reversed()
}

// RTTYTakeLines drains decoded text lines.
func (r *Radio) RTTYTakeLines() []string {
	return r.rtty.TakeLines()
}

// RTTYCurrent returns the partially-received line.
func (r *Radio) RTTYCurrent() string {
	return r.rtty.Current()
}

// RTTYLevels returns mark/space tuning levels (0..~1).
func (r *Radio) RTTYLevels() (float64, float64) {
	return r.rtty.Levels()
}

// FT8Enabled reports whether FT8 detection is active.
func (r *Radio) FT8Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ft8On
}

// SetFT8Enabled toggles FT8 detection. Turning it on also switches the
// demod to USB (FT8 convention; SetMode enforces the lock from then on).
func (r *Radio) SetFT8Enabled(on bool) {
	r.mu.Lock()
	r.ft8On = on
	r.ft8.SetEnabled(on)
	chain := r.chain
	needUSB := on && r.mode.Name != dsp.ModeUSB.Name
	r.mu.Unlock()
	if chain != nil {
		if on {
			chain.SetFT8Detector(r.ft8)
		} else {
			chain.SetFT8Detector(nil)
		}
	}
	if needUSB {
		// SetMode rebuilds the chain and re-attaches the detector.
		r.SetMode(dsp.ModeUSB)
	}
	fmt.Fprintf(os.Stderr, "radio: FT8 %v\n", on)
}

// AISRFEnabled reports whether the over-the-air AIS demodulator runs.
func (r *Radio) AISRFEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.aisRFOn
}

// SetAISRFEnabled toggles the over-the-air AIS decoder. Turning it on
// tunes the receiver midway between both AIS channels (162.000 MHz —
// they sit at ±25 kHz) and locks the mode to NFM, mirroring the FT8
// USB convention. FT8 is turned off first: both fight over the tuner.
func (r *Radio) SetAISRFEnabled(on bool) {
	if on && r.FT8Enabled() {
		r.SetFT8Enabled(false)
	}
	r.mu.Lock()
	r.aisRFOn = on
	chain := r.chain
	needNFM := on && r.mode.Name != dsp.ModeNFM.Name
	r.mu.Unlock()
	if chain != nil {
		if on {
			chain.SetAISDemods(r.aisA, r.aisB)
		} else {
			chain.SetAISDemods(nil, nil)
		}
	}
	if needNFM {
		r.SetMode(dsp.ModeNFM) // rebuilds the chain and re-attaches
	}
	if on && r.LO() != 162_000_000 {
		r.SetFreq(162_000_000)
	}
	fmt.Fprintf(os.Stderr, "radio: AIS RF %v"+string(rune(10)), on)
}

// SetLocalMute silences the handheld's own speaker while the web audio
// tap keeps streaming the full demodulated signal.
func (r *Radio) SetLocalMute(m bool) {
	r.mu.Lock()
	r.localMute = m
	r.mu.Unlock()
}

// LocalMuted reports the speaker-mute state.
func (r *Radio) LocalMuted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.localMute
}

// SetAudioTap installs a callback receiving the demodulated mono audio
// (post squelch/NR/filters) at the mode's output rate — the web audio
// stream. nil detaches. Called from the DSP goroutine.
func (r *Radio) SetAudioTap(f func(mono []float32, rate int)) {
	r.mu.Lock()
	r.audioTap = f
	r.mu.Unlock()
}

// SetAISPayloadFunc installs the receiver for demodulated AIS payloads
// (called from the DSP goroutine).
func (r *Radio) SetAISPayloadFunc(f func(payload []byte, ch int, levelDb float64)) {
	r.mu.Lock()
	r.aisPay = f
	r.mu.Unlock()
}

// SyncFT8 marks now as end of a transmission.
func (r *Radio) SyncFT8() {
	r.mu.Lock()
	det := r.ft8
	r.mu.Unlock()
	if det != nil {
		det.Sync()
	}
}

// FT8Synced reports whether user has synced.
func (r *Radio) FT8Synced() bool {
	r.mu.Lock()
	det := r.ft8
	r.mu.Unlock()
	return det != nil && det.IsSynced()
}

// FT8Results returns latest detections.
func (r *Radio) FT8Results() []dsp.FT8Detection {
	r.mu.Lock()
	det := r.ft8
	r.mu.Unlock()
	if det == nil {
		return nil
	}
	return det.Results()
}

// FT8TakeMessages drains newly decoded FT8 messages since the last
// call (for the history window — the live results only survive one
// scan cycle).
func (r *Radio) FT8TakeMessages() []dsp.FT8Message {
	r.mu.Lock()
	det := r.ft8
	r.mu.Unlock()
	if det == nil {
		return nil
	}
	return det.TakeMessages()
}

var ft8Busy bool

// FT8Process runs detector analysis in a background goroutine.
func (r *Radio) FT8Process() {
	r.mu.Lock()
	det := r.ft8
	r.mu.Unlock()
	if det == nil || !det.Enabled() || ft8Busy {
		return
	}
	ft8Busy = true
	go func() {
		defer func() { ft8Busy = false }()
		det.Process()
	}()
}

// Hostname returns the host label the UI should display.
func (r *Radio) Hostname() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.usbSrc {
		return "USB"
	}
	return r.Host
}

// GainText describes the current gain setting for the UI (in dB).
// directSamplingFor returns the direct-sampling command value for a
// frequency under the current preference (-1 auto / 0 off / 2 on).
func (r *Radio) directSamplingFor(hz int64) int {
	switch r.dsMode {
	case 0:
		return 0
	case 2:
		return 2
	default:
		if hz < 24_000_000 {
			return 2
		}
		return 0
	}
}

// AGCEnabled reports whether SSB/CW AGC is on.
func (r *Radio) AGCEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agcOn
}

// SetAGCEnabled toggles the SSB/CW AGC live (also applied to future
// chain rebuilds).
func (r *Radio) SetAGCEnabled(on bool) {
	r.mu.Lock()
	r.agcOn = on
	chain := r.chain
	r.mu.Unlock()
	if chain != nil {
		chain.SetAGCEnabled(on)
	}
}

// DirectSamplingMode returns the preference: -1 auto, 0 off, 2 on.
func (r *Radio) DirectSamplingMode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dsMode
}

// DirectSamplingLabel names the preference for the menu.
func (r *Radio) DirectSamplingLabel() string {
	switch r.DirectSamplingMode() {
	case 0:
		return i18n.T("ds_off")
	case 2:
		return i18n.T("ds_on")
	default:
		return i18n.T("ds_auto")
	}
}

// SetDirectSamplingMode stores the preference and applies it live
// (re-sending the frequency so the tuner path re-arms after a switch).
func (r *Radio) SetDirectSamplingMode(mode int) {
	if mode != -1 && mode != 0 && mode != 2 {
		return
	}
	r.mu.Lock()
	r.dsMode = mode
	want := r.directSamplingFor(r.freqHz)
	switchNow := want != r.hfApplied
	r.hfApplied = want
	client := r.client
	freq := r.freqHz
	r.mu.Unlock()
	if client != nil && switchNow {
		if err := client.SetDirectSampling(want); err != nil {
			fmt.Fprintf(os.Stderr, "radio: direct sampling %d failed: %v\n", want, err)
			client.Close()
			return
		}
		_ = client.SetFrequency(uint32(freq))
	}
	fmt.Fprintf(os.Stderr, "radio: direct sampling preference %d\n", mode)
}

func (r *Radio) GainText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gainDb < 0 {
		return "GAIN AGC"
	}
	return fmt.Sprintf("GAIN %.1fdB", r.gainDb)
}

// Bandwidths returns the selectable bandwidths (Hz) for the current mode.
func (r *Radio) Bandwidths() []float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode.Bandwidths()
}

// Bandwidth returns the current channel bandwidth in Hz.
// PassbandHz returns the live receive passband edges in Hz relative
// to the listening frequency — the device bracket and the web channel
// overlay draw from this so they match what the filter actually passes.
func (r *Radio) PassbandHz() (float64, float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.chain == nil {
		return -6000, 6000
	}
	return r.chain.PassbandHz()
}

func (r *Radio) Bandwidth() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mode.BwHz
}

// SetBandwidth applies a new channel bandwidth to the current mode (pure
// DSP — the chain is rebuilt; no server interaction).
func (r *Radio) SetBandwidth(bw float64) {
	r.mu.Lock()
	m := r.mode
	if bw <= 0 {
		return
	}
	for _, v := range m.Bandwidths() {
		if v == bw {
			m.BwHz = bw
			break
		}
	}
	r.mode = m
	r.chain = dsp.NewChain(m, r.tap, r.rawTap)
	r.chain.SetVolume(r.vol)
	r.chain.SetSquelchDb(r.sqlDb)
	r.chain.SetAGCEnabled(r.agcOn)
	r.applyAudioFx()
	if r.ft8On {
		r.chain.SetFT8Detector(r.ft8)
	}
	// Fresh chain = offset 0; keep listening where the dial says.
	if off := r.freqHz - r.loHz; off != 0 {
		r.chain.SetOffsetHz(float64(off))
	}
	r.mu.Unlock()
	// Wide SSB bandwidths change the chain's audio rate (8k ↔ 16k).
	if r.out != nil {
		r.out.SetInputRate(m.AudioOutRate())
	}
	fmt.Fprintf(os.Stderr, "radio: bandwidth %.4g Hz (%s)\n", bw, m.Name)
}

// GainDb returns the current tuner gain in dB (-1 = AGC).
func (r *Radio) GainDb() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gainDb
}

// SetGainDb applies a new tuner gain live: stored for the next connect and
// pushed to a connected dongle at once (mid-stream gain commands are fine —
// the earlier ban was an endianness misdiagnosis).
func (r *Radio) SetGainDb(db float64) {
	if db < 0 {
		db = 0
	}
	if db > 49.6 {
		db = 49.6
	}
	r.mu.Lock()
	r.gainDb = db
	client := r.client
	r.mu.Unlock()
	if client == nil {
		return
	}
	if err := client.SetTunerAGC(false); err != nil {
		client.Close()
		return
	}
	if err := client.SetGainTenthsDB(int32(math.Round(db * 10))); err != nil {
		client.Close() // poisoned stream — reconnect with the new gain
	}
}

// GainStepDb moves db one entry up (dir>0) or down the RTL-SDR V4 gain
// table, snapping to the nearest current entry first.
func GainStepDb(db float64, dir int) float64 {
	idx := GainDbToIndex(db)
	idx += dir
	if idx < 0 {
		idx = 0
	}
	if idx >= len(r828dGains) {
		idx = len(r828dGains) - 1
	}
	return r828dGains[idx]
}

// SquelchDb returns the current NFM squelch threshold (40 = off).
func (r *Radio) SquelchDb() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sqlDb
}

// SetSquelchDb applies a new NFM squelch threshold in absolute dBFS
// (clamped −100..0; ≥ 0 = off — full scale is unreachable). Legacy
// configs from the old relative scheme stored positive values: "off"
// (40) stays off, and the relative presets map to −40 dBFS, a quiet
// default the user can then fine-tune against the meter.
func (r *Radio) SetSquelchDb(db float64) {
	if db > 0 {
		if db >= 40 {
			db = 0
		} else {
			db = -40
		}
	}
	if db < -100 {
		db = -100
	}
	r.mu.Lock()
	r.sqlDb = db
	r.chain.SetSquelchDb(db)
	r.mu.Unlock()
}

// Snapshot is the per-frame status for the UI.
type Snapshot struct {
	Connected   bool
	Connecting  bool
	StatusText  string
	PowerDb     float64
	SquelchOpen bool
	BytesRx     uint64
}

func (r *Radio) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Snapshot{BytesRx: r.bytesRx}
	switch r.state {
	case stateStreaming:
		s.Connected = true
	case stateConnecting:
		s.Connecting = true
		s.StatusText = i18n.T("connecting") + r.Host
	default:
		s.StatusText = i18n.T("disconnected") + r.lastErr
		if r.reconAt.After(time.Now()) {
			s.StatusText += fmt.Sprintf(i18n.T("retry_in"), int(time.Until(r.reconAt).Seconds())+1)
		}
	}
	if r.chain != nil {
		s.PowerDb = r.chain.PowerDb()
		s.SquelchOpen = r.chain.SquelchOpen()
	}
	return s
}
