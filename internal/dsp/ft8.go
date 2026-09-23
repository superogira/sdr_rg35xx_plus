package dsp

import (
	"fmt"
	"math"
	"os"
	"sync"
)

// FT8 constants
const (
	FT8ToneHz     = 6.25
	FT8SymMs      = 160
	FT8NumSymbols = 79
	FT8AudioRate  = 8000
	FT8SymSamples = FT8AudioRate * FT8SymMs / 1000 // 1280
	FT8FrameSamp  = FT8NumSymbols * FT8SymSamples
)

var ft8SyncCostas = [7]int{3, 1, 4, 0, 6, 5, 2}

// ft8SyncPos lists all 21 Costas symbol positions: three blocks of 7
// at 0-6, 36-42 and 72-78 (FT8_SYNC_OFFSET = 36), each block repeating
// the same pattern. (The old {0,36,37,38,72,73,74} map was wrong and
// could never reliably pass a sync check.)
func ft8SyncPos(i int) int {
	if i < 7 {
		return i
	}
	if i < 14 {
		return 36 + i - 7
	}
	return 72 + i - 14
}

// FT8Detection is one detected FT8 signal.
type FT8Detection struct {
	FreqHz     float64
	SNRDb      float64
	Confidence float64
	Message    *FT8Message
	// Diag explains a failed decode ("ldpc=N" / "crc:...") for the log.
	Diag string
}

// FT8Detector streams 8 kHz audio into an STFT waterfall and decodes
// FT8 messages via soft Costas correlation + LDPC.
type FT8Detector struct {
	mu        sync.Mutex
	enabled   bool
	results   []FT8Detection
	synced    bool
	// wf consumes audio incrementally; wfPending buffers sub-640-sample
	// remainders between Feed calls.
	wf        *ft8Waterfall
	wfPending []float64
	// pending holds decoded messages until the UI drains them. The
	// live results slice is overwritten on every scan (~1 s), so a
	// decode only survives there for a scan or two — polling it at
	// 15 s intervals missed nearly every message.
	pending []FT8Message
}

const ft8RingSamples = 8000 * 15

// Package-level FFT scratch (reused, zero-alloc in steady state).
var (
	ft8FFTRe   []float64
	ft8FFTIm   []float64
	ft8Mags    []float64
	ft8SortBuf []float64
)

func NewFT8Detector() *FT8Detector {
	return &FT8Detector{wf: newFT8Waterfall(), wfPending: make([]float64, 0, 1024)}
}

func (d *FT8Detector) SetEnabled(on bool) {
	d.mu.Lock()
	d.enabled = on
	if on {
		d.wf.reset()
		d.wfPending = d.wfPending[:0]
		d.synced = false
	}
	d.mu.Unlock()
}

func (d *FT8Detector) Enabled() bool  { d.mu.Lock(); defer d.mu.Unlock(); return d.enabled }
func (d *FT8Detector) IsSynced() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.synced }

// Sync acknowledges a manual slot sync (Y button); the marker drawing
// in the UI is driven from the button press itself.
func (d *FT8Detector) Sync() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return
	}
	d.synced = true
}

func (d *FT8Detector) Feed(samples []float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return
	}
	d.wfPending = append(d.wfPending, samples...)
	d.wf.feed(d.wfPending)
	// keep the unconsumed remainder (< 640 samples)
	n := len(d.wfPending) / FT8SymSamples * FT8SymSamples
	d.wfPending = append(d.wfPending[:0], d.wfPending[n:]...)
}

func (d *FT8Detector) Results() []FT8Detection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]FT8Detection(nil), d.results...)
}

// TakeMessages drains the queue of decoded messages (the UI history
// window consumes these).
func (d *FT8Detector) TakeMessages() []FT8Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pending) == 0 {
		return nil
	}
	out := d.pending
	d.pending = nil
	return out
}

// Process scans the waterfall for Costas patterns and decodes every
// candidate. The correlation search is the expensive part (~1 s on the
// A53 at 2×2 oversampling); the detector's busy guard keeps it off the
// UI thread.
func (d *FT8Detector) Process() {
	d.mu.Lock()
	if !d.enabled || d.wf.count < 80 {
		d.mu.Unlock()
		return
	}
	wf := d.wf
	d.mu.Unlock()

	var newResults []FT8Detection
	cands := wf.findCandidates(140, 10)
	// Several (time,freq) cells can match the same transmission in one
	// scan — queue each distinct message only once.
	seen := map[string]bool{}
	for _, c := range cands {
		llr := make([]float64, 174)
		wf.extractLLR(c, llr)
		msg, diag := ft8DecodeCodeword(llr)
		det := FT8Detection{
			FreqHz:     c.candFreqHz(),
			SNRDb:      wf.candSNRDb(c),
			Confidence: float64(c.score) / 255,
			Diag:       diag,
		}
		if msg != nil && msg.Valid {
			if seen[msg.Text] {
				continue
			}
			seen[msg.Text] = true
			msg.SNRDb = det.SNRDb
			det.Message = msg
			newResults = append(newResults, det)
			continue
		}
		newResults = append(newResults, det)
	}
	if len(newResults) > 10 {
		newResults = newResults[:10]
	}
	dStr := ""
	for _, r := range newResults {
		dStr += fmt.Sprintf(" %.0fHz/%.0fdB", r.FreqHz, r.SNRDb)
		if r.Message != nil && r.Message.Valid {
			dStr += fmt.Sprintf(" \"%s\"", r.Message.Text)
		} else if r.Diag != "" {
			dStr += fmt.Sprintf(" (%s)", r.Diag)
		}
	}
	fmt.Fprintf(os.Stderr, "ft8: blocks=%d cand=%d det=%d%s\n", wf.count, len(cands), len(newResults), dStr)

	d.mu.Lock()
	d.results = newResults
	for _, r := range newResults {
		if r.Message != nil && r.Message.Valid {
			d.pending = append(d.pending, *r.Message)
		}
	}
	if len(d.pending) > 64 {
		d.pending = d.pending[len(d.pending)-64:]
	}
	d.mu.Unlock()
}

// findCandidates uses a short FFT to find energy clusters.

func goertzelMag(samples []float64, freqHz, sampleRate float64) float64 {
	k := 2 * math.Pi * freqHz / sampleRate
	coeff := 2 * math.Cos(k)
	var s0, s1, s2 float64
	for _, x := range samples {
		s0 = x + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}
	return math.Sqrt(s1*s1 + s2*s2 - coeff*s1*s2)
}

func growF(s []float64, n int) []float64 {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]float64, n)
}

// Goertzel exposes goertzelMag for external tools.
func Goertzel(samples []float64, freqHz, sampleRate float64) float64 {
	return goertzelMag(samples, freqHz, sampleRate)
}
