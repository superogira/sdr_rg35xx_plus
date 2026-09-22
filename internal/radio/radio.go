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

	mu     sync.Mutex
	freqHz int64
	mode   dsp.Mode
	gainDb float64 // tuner gain in dB at connect; negative = AGC
	vol    float64
	sqlDb  float64 // NFM squelch threshold above floor (40 = off)
	iqRate int     // capture sample rate in Hz (server default 2.048M)
	hfMode bool    // direct sampling active (below 24 MHz)

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
	if hz != 2_048_000 && hz != 1_024_000 {
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
	client := r.client
	r.mu.Unlock()
	if r.out != nil {
		r.out.SetInputRate(dsp.AudioRate)
	}
	if client != nil {
		client.Close() // reconnect with the new rate
	}
	fmt.Fprintf(os.Stderr, "radio: capture rate now %d Hz (IF2 %d, audio %d)'+chr(92)+'n", hz, dsp.IF2Rate, dsp.AudioRate)
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
			fmt.Fprintf(os.Stderr, "radio: requested %d Hz sample rate'+chr(92)+'n", r.iqRate)
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
		r.hfMode = freq < 24_000_000
		hf := r.hfMode
		r.mu.Unlock()
		if hf {
			if err := client.SetDirectSampling(2); err != nil {
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
			r.mu.Unlock()
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
	hf := hz < 24_000_000
	switchHF := hf != r.hfMode
	r.hfMode = hf
	r.mu.Unlock()
	if client == nil {
		return
	}
	if switchHF {
		// Crossed the tuner/direct-sampling boundary: the V4 receives HF
		// through its internal mux on the Q branch.
		mode := 0
		if hf {
			mode = 2
		}
		if err := client.SetDirectSampling(mode); err != nil {
			fmt.Fprintf(os.Stderr, "radio: direct sampling %d failed: %v\n", mode, err)
			client.Close()
			return
		}
		fmt.Fprintf(os.Stderr, "radio: direct sampling mode %d (HF=%v)\n", mode, hf)
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
	r.mu.Unlock()
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

// Hostname returns the host label the UI should display.
func (r *Radio) Hostname() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Host
}

// GainText describes the current gain setting for the UI (in dB).
func (r *Radio) GainText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gainDb < 0 {
		return "GAIN AGC"
	}
	return fmt.Sprintf("GAIN %.1fdB", r.gainDb)
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
		s.StatusText = "กำลังเชื่อมต่อ " + r.Host
	default:
		s.StatusText = "ขาดการเชื่อมต่อ: " + r.lastErr
		if r.reconAt.After(time.Now()) {
			s.StatusText += fmt.Sprintf(" (ลองใหม่ใน %ds)", int(time.Until(r.reconAt).Seconds())+1)
		}
	}
	if r.chain != nil {
		s.PowerDb = r.chain.PowerDb()
		s.SquelchOpen = r.chain.SquelchOpen()
	}
	return s
}
