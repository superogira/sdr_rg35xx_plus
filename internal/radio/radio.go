// Package radio owns the rtl_tcp connection lifecycle: connect, configure
// the dongle, stream IQ through the DSP into the audio pipe, reconnect with
// backoff, and apply live parameter changes (frequency/mode/gain).
package radio

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

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

	mu        sync.Mutex
	freqHz    int64
	mode      dsp.Mode
	gainDb    float64 // tuner gain in dB at connect; negative = AGC
	vol       float64
	sqlDb     float64 // NFM squelch threshold above floor (40 = off)
	iqRate    int     // capture sample rate in Hz (server default 2.048M)
	dsMode    int     // -1 auto (DS below 24 MHz), 0 force off, 2 force on (Q)
	agcOn     bool    // SSB/CW AGC enabled
	ft8On     bool
	ft8       *dsp.FT8Detector
	hfApplied int // direct-sampling mode currently set on the server

	client  *rtltcp.Client
	gains   int32
	chain   *dsp.Chain
	state   int // 0 disconnected, 1 connecting, 2 streaming
	lastErr string
	reconAt time.Time
	bytesRx uint64
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
	return &Radio{
		Host:   host,
		tap:    dsp.NewSpectrumTap(),
		rawTap: dsp.NewSpectrumTap(),
		out:    out,
		freqHz: freqHz,
		mode:   mode,
		gainDb: gainDb,
		vol:    1,
		sqlDb:  8,
		iqRate: 2_048_000,
		agcOn:  true,
		ft8:    dsp.NewFT8Detector(),
		chain:  dsp.NewChain(mode, nil, nil),
	}
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
// 1.024M — both verified against the server). The whole DSP chain is
// re-dimensioned and the client dropped, so the next session opens with
// a SetSampleRate as its very first command — the only rate-change
// pattern the server tolerates (mid-stream changes destabilize it).
func (r *Radio) SetCaptureRate(hz int) {
	if hz != 2_048_000 && hz != 1_024_000 && hz != 512_000 {
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
	client := r.client
	r.mu.Unlock()
	if r.out != nil {
		// The chain's audio rate changed with the capture rate —
		// re-tune the resampler to the MODE's rate (dsp.AudioRate is
		// the old fixed 32k constant, wrong at 1.024M/512k).
		r.out.SetInputRate(r.mode.AudioOutRate())
	}
	if client != nil {
		client.Close() // reconnect with the new rate
	}
	fmt.Fprintf(os.Stderr, "radio: capture rate now %d Hz (IF2 %d, audio %d)\n", hz, dsp.IF2Rate, dsp.AudioRate)
}

// NewDemo builds a Radio whose Run generates a synthetic signal in-process
// instead of connecting anywhere (display/audio development without a
// dongle).
func NewDemo(mode dsp.Mode, out *audio.Output) *Radio {
	r := New("DEMO (สัญญาณจำลอง)", 145_500_000, mode, -1, out)
	r.demo = true
	return r
}

// Run is the connection loop; it returns when ctx is done.
func (r *Radio) Run(ctx context.Context) {
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
			if r.out != nil {
				r.out.WriteAudio(audio)
			}
		})
		return
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := r.session(ctx); err != nil {
			r.mu.Lock()
			r.state = stateDisconnected
			r.lastErr = err.Error()
			r.reconAt = time.Now().Add(backoff)
			r.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 8*time.Second {
				backoff *= 2
			}
			// Feed silence to the player while waiting so it never
			// underruns audibly.
			if r.out != nil {
				r.out.Silence(int(backoff.Milliseconds()))
			}
			continue
		}
		backoff = time.Second
	}
}

// session runs one connect/stream cycle. Returns nil only on ctx done.
func (r *Radio) session(ctx context.Context) error {
	r.mu.Lock()
	r.state = stateConnecting
	r.lastErr = ""
	r.mu.Unlock()

	client, err := rtltcp.Dial(r.Host, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect: %v", err)
	}

	setup := func() error {
		r.mu.Lock()
		r.client = client
		r.gains = client.Info.GainCount
		chain := dsp.NewChain(r.mode, r.tap, r.rawTap)
		chain.SetVolume(r.vol)
		chain.SetSquelchDb(r.sqlDb)
		chain.SetAGCEnabled(r.agcOn)
		r.chain = chain
		r.state = stateStreaming
		freq, gainDb := r.freqHz, r.gainDb
		r.mu.Unlock()
		chain.Reset()

		if r.iqRate != 2_048_000 {
			// The server accepts exactly one rate setting per connection
			// and must see it before anything else.
			if err := client.SetSampleRate(uint32(r.iqRate)); err != nil {
				return fmt.Errorf("sample rate: %w", err)
			}
			fmt.Fprintf(os.Stderr, "radio: requested %d Hz sample rate\n", r.iqRate)
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
		want := r.directSamplingFor(freq)
		r.hfApplied = want
		r.mu.Unlock()
		if want != 0 {
			if err := client.SetDirectSampling(want); err != nil {
				return fmt.Errorf("direct sampling: %w", err)
			}
		}
		if err := client.SetFrequency(uint32(freq)); err != nil {
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
		client.Close()
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
					return errors.New("server ส่งข้อมูลเปล่า (ต้องรีสตาร์ท rtl_tcp server)")
				}
			} else {
				flatBlocks = 0
			}
			r.mu.Lock()
			chain := r.chain
			r.mu.Unlock()
			audioBuf = audioBuf[:0]
			chain.Process(buf[:n], &audioBuf)
			if r.out != nil {
				r.out.WriteAudio(audioBuf)
			}
			r.mu.Lock()
			r.bytesRx += uint64(n)
			total := r.bytesRx
			r.mu.Unlock()
			if !rateDone {
				if rateT0.IsZero() && total >= 512*1024 {
					rateT0 = time.Now()
					rateB0 = total
				} else if !rateT0.IsZero() && time.Since(rateT0) >= 2*time.Second {
					actual := float64(total-rateB0) / time.Since(rateT0).Seconds() / 2
					snap := int(math.Round(actual/32000) * 32000)
					if snap < 512_000 {
						snap = 512_000
					}
					if snap > 3_200_000 {
						snap = 3_200_000
					}
					// A just-reconnected server ramps up slowly; if
					// the measurement is wildly below the configured
					// rate, it is a startup transient — trust the
					// config and re-measure later instead of
					// re-dimensioning to a nonsense rate.
					if float64(snap) < float64(dsp.IQRate)*0.7 || float64(snap) > float64(dsp.IQRate)*1.3 {
						fmt.Fprintf(os.Stderr, "radio: measured %.3f Msps vs configured %d — transient, keeping config\n", actual, dsp.IQRate)
						rateDone = true
						sessT0 = rateT0
						lastDropChk = time.Now()
						break
					}
					r.mu.Lock()
					configured := r.iqRate
					r.mu.Unlock()
					if dsp.IQRate != snap {
						fmt.Fprintf(os.Stderr, "radio: stream measures %.3f Msps — re-dimensioning DSP from %d to %d Hz\n", actual/1e6, dsp.IQRate, snap)
						dsp.SetIQRate(snap)
						r.SetMode(r.Mode()) // rebuilds the chain + resampler rate
					} else {
						fmt.Fprintf(os.Stderr, "radio: stream rate confirmed %d Hz (%.3f Msps measured)\n", snap, actual/1e6)
					}
					_ = configured
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
	r.freqHz = hz
	client := r.client
	want := r.directSamplingFor(hz)
	switchHF := want != r.hfApplied
	r.hfApplied = want
	r.mu.Unlock()
	if client == nil {
		return
	}
	if switchHF {
		if err := client.SetDirectSampling(want); err != nil {
			fmt.Fprintf(os.Stderr, "radio: direct sampling %d failed: %v\n", want, err)
			client.Close()
			return
		}
		fmt.Fprintf(os.Stderr, "radio: direct sampling mode %d (freq %.4f MHz)\n", want, float64(hz)/1e6)
	}
	if err := client.SetFrequency(uint32(hz)); err != nil {
		fmt.Fprintf(os.Stderr, "radio: tune %d failed: %v — reconnecting\n", hz, err)
		client.Close()
		return
	}
	// Logged so the app log can be compared against the rtl_tcp server's
	// own console when a tuning glitch is investigated.
	fmt.Fprintf(os.Stderr, "radio: tune %d (%.4f MHz)\n", hz, float64(hz)/1e6)
}

func (r *Radio) Freq() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.freqHz
}

// SetMode swaps the demod chain. The dongle needs no reconfiguration (same
// sample rate); only the DSP changes.
func (r *Radio) SetMode(mode dsp.Mode) {
	r.mu.Lock()
	r.mode = mode
	r.chain = dsp.NewChain(mode, r.tap, r.rawTap)
	r.chain.SetVolume(r.vol)
	r.chain.SetSquelchDb(r.sqlDb)
	r.chain.SetAGCEnabled(r.agcOn)
	r.mu.Unlock()
	// SSB/CW chains produce 8 kHz audio; FM modes IF2/4.
	if r.out != nil {
		r.out.SetInputRate(mode.AudioOutRate())
	}
}

// CycleSquelch steps the NFM squelch threshold 4 → 8 → 12 → 16 → off and
// returns the new label for the UI. Pure DSP — no server commands.
func (r *Radio) CycleSquelch() string {
	r.mu.Lock()
	var next float64
	switch r.sqlDb {
	case 4:
		next = 8
	case 8:
		next = 12
	case 12:
		next = 16
	case 16:
		next = 40
	default:
		next = 4
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
	if r.sqlDb >= 40 {
		return "SQL OFF"
	}
	return fmt.Sprintf("SQL %.0fdB", r.sqlDb)
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
func (r *Radio) SetHost(host string) {
	r.mu.Lock()
	if r.Host == host {
		r.mu.Unlock()
		return
	}
	r.Host = host
	client := r.client
	r.mu.Unlock()
	if client != nil {
		client.Close() // reconnect to the new address
	}
	fmt.Fprintf(os.Stderr, "radio: host now %s (reconnecting)\n", host)
}

// FT8Enabled reports whether FT8 detection is active.
func (r *Radio) FT8Enabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ft8On
}

// SetFT8Enabled toggles FT8 detection.
func (r *Radio) SetFT8Enabled(on bool) {
	r.mu.Lock()
	r.ft8On = on
	r.ft8.SetEnabled(on)
	chain := r.chain
	r.mu.Unlock()
	if chain != nil {
		if on {
			chain.SetFT8Detector(r.ft8)
		} else {
			chain.SetFT8Detector(nil)
		}
	}
	fmt.Fprintf(os.Stderr, "radio: FT8 %v\n", on)
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
	r.mu.Unlock()
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

// SetSquelchDb applies a new NFM squelch threshold (clamped 4-40; 40 = off).
func (r *Radio) SetSquelchDb(db float64) {
	if db < 4 {
		db = 4
	}
	if db > 40 {
		db = 40
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
