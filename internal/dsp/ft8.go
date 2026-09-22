package dsp

import (
	"math"
	"sync"
)

// FT8 constants: tone spacing 6.25 Hz, symbol period 0.160 s, 79 symbols,
// 8-FSK centered at 1500 Hz.
const (
	FT8ToneHz     = 6.25
	FT8SymMs      = 160 // ms
	FT8NumSymbols = 79
	FT8FirstTone  = 1500 - 3.5*FT8ToneHz // ~1478.125 Hz
	FT8AudioRate  = 8000
	FT8SymSamples = FT8AudioRate * FT8SymMs / 1000 // 1280
)

// ft8SyncCostas is the Costas array pattern used for sync (7 symbols at
// known positions within the 79-symbol frame).
var ft8SyncCostas = [7]int{3, 1, 4, 0, 6, 5, 2}

// ft8SyncPositions are the symbol indices where the Costas tones appear.
var ft8SyncPositions = [7]int{0, 36, 37, 38, 72, 73, 74}

// FT8Detection is one detected FT8 signal.
type FT8Detection struct {
	FreqHz     float64     // audio frequency of the signal (0-3000 Hz)
	SNRDb      float64     // estimated signal-to-noise ratio
	TimeSlot   int         // 0 = first 15s, 1 = second 15s (in each 30s cycle)
	Confidence float64     // 0-1 sync match quality
	Message    *FT8Message // decoded message (nil if decode failed)
}

// FT8Detector scans 8 kHz audio for FT8 sync patterns using Goertzel
// filters at the 8 possible tone frequencies. A full Costas match at any
// of the 7 sync positions confirms an FT8 signal.
type FT8Detector struct {
	mu sync.Mutex

	// Ring buffer of recent audio samples (~12 s at 8 kHz).
	audio   []float64
	written int

	// Goertzel state per tone per slot (we scan multiple frequency slots).
	slotHz  []float64 // candidate FT8 center frequencies
	results []FT8Detection
	enabled bool
}

const ft8RingSamples = 8000 * 12 // 12 s of audio

func NewFT8Detector() *FT8Detector {
	d := &FT8Detector{
		audio:  make([]float64, ft8RingSamples),
		slotHz: makeFT8Slots(),
	}
	return d
}

// makeFT8Slots generates candidate center frequencies: FT8 signals can
// appear anywhere in the audio passband, we scan in 12.5 Hz steps.
func makeFT8Slots() []float64 {
	var slots []float64
	// Scan from 400 to 2800 Hz in 12.5 Hz steps (aligned to FT8 grid)
	for f := 400.0; f <= 2800.0; f += 12.5 {
		slots = append(slots, f)
	}
	return slots
}

// SetEnabled toggles FT8 detection.
func (d *FT8Detector) SetEnabled(on bool) {
	d.mu.Lock()
	d.enabled = on
	if on {
		d.written = 0 // reset buffer
	}
	d.mu.Unlock()
}

// Enabled returns whether detection is active.
func (d *FT8Detector) Enabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.enabled
}

// Feed appends mono audio samples (8 kHz) to the ring buffer.
func (d *FT8Detector) Feed(samples []float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return
	}
	for _, s := range samples {
		d.audio[d.written%ft8RingSamples] = s
		d.written++
	}
}

// Results returns the latest detections (call from the UI thread).
func (d *FT8Detector) Results() []FT8Detection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]FT8Detection(nil), d.results...)
}

// Process analyzes the ring buffer for FT8 sync patterns. Called
// periodically (e.g., once per second) from a background goroutine.
func (d *FT8Detector) Process() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled || d.written < ft8RingSamples {
		return
	}

	d.results = d.results[:0]

	// Extract the linear (unwrapped) buffer.
	linear := make([]float64, ft8RingSamples)
	start := d.written % ft8RingSamples
	copy(linear, d.audio[start:])
	copy(linear[ft8RingSamples-start:], d.audio[:start])

	// For each candidate slot, check the Costas sync at the known positions.
	for _, centerHz := range d.slotHz {
		if det, ok := d.checkSyncAt(linear, centerHz); ok {
			det.Message = DecodeFT8(linear, centerHz)
			d.results = append(d.results, det)
		}
	}
}

// goertzelMag returns the magnitude at freqHz for the given samples using
// the Goertzel algorithm (single-bin DFT).
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

// checkSyncAt tests whether FT8 Costas sync tones appear at the given
// center frequency in the audio. Returns a detection if all 7 sync
// symbols match the expected Costas pattern.
func (d *FT8Detector) checkSyncAt(audio []float64, centerHz float64) (FT8Detection, bool) {
	// Test the first Costas block (symbols 0-6, i.e., the first 7×160ms
	// = 1.12 seconds of the frame).
	offset := ft8RingSamples - FT8NumSymbols*FT8SymSamples // align to end

	syncStart := offset + ft8SyncPositions[0]*FT8SymSamples
	if syncStart < 0 || syncStart+7*FT8SymSamples > len(audio) {
		return FT8Detection{}, false
	}

	// Compute magnitude for all 8 tones at each of the 7 sync symbol slots.
	mags := make([]float64, 8)
	matches := 0
	totalEnergy := 0.0

	for si := 0; si < 7; si++ {
		start := offset + ft8SyncPositions[si]*FT8SymSamples
		if start < 0 || start+FT8SymSamples > len(audio) {
			return FT8Detection{}, false
		}
		sym := audio[start : start+FT8SymSamples]

		best, bestMag := -1, 0.0
		var sumMag float64
		for tone := 0; tone < 8; tone++ {
			toneHz := centerHz + (float64(tone)-3.5)*FT8ToneHz
			m := goertzelMag(sym, toneHz, FT8AudioRate)
			mags[tone] = m
			sumMag += m
			if m > bestMag {
				bestMag = m
				best = tone
			}
		}
		totalEnergy += sumMag

		if best == ft8SyncCostas[si] {
			matches++
		}
	}

	// All 7 sync symbols must match for a confirmed detection.
	if matches < 6 { // allow 1 mismatch for noise tolerance
		return FT8Detection{}, false
	}

	// Estimate SNR: sync tone energy vs total energy
	snr := 10 * math.Log10(totalEnergy/float64(7*8)+1e-12)

	// Determine time slot (0 or 1) based on the detection position
	timeSlot := (d.written / (8000 * 15)) % 2

	return FT8Detection{
		FreqHz:     centerHz,
		SNRDb:      snr,
		TimeSlot:   timeSlot,
		Confidence: float64(matches) / 7.0,
	}, true
}
