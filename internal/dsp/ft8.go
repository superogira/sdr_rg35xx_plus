package dsp

import (
	"math"
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
var ft8SyncPositions = [7]int{0, 36, 37, 38, 72, 73, 74}

// FT8Detection is one detected FT8 signal.
type FT8Detection struct {
	FreqHz     float64
	SNRDb      float64
	Confidence float64
	Message    *FT8Message
}

// FT8Detector buffers 15 s of 8 kHz audio, scans for FT8 sync.
type FT8Detector struct {
	mu        sync.Mutex
	audio     []float64
	written   int64
	results   []FT8Detection
	enabled   bool
	linear    []float64
	slotStart int64
	synced    bool
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
	return &FT8Detector{audio: make([]float64, ft8RingSamples)}
}

func (d *FT8Detector) SetEnabled(on bool) {
	d.mu.Lock()
	d.enabled = on
	if on {
		d.written = 0
		d.synced = false
	}
	d.mu.Unlock()
}

func (d *FT8Detector) Enabled() bool  { d.mu.Lock(); defer d.mu.Unlock(); return d.enabled }
func (d *FT8Detector) IsSynced() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.synced }

// Sync marks now as end of a transmission; next slot starts 2.36 s later.
func (d *FT8Detector) Sync() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return
	}
	gap := int64(2360 * FT8AudioRate / 1000)
	d.slotStart = d.written + gap
	d.synced = true
}

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

func (d *FT8Detector) Results() []FT8Detection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]FT8Detection(nil), d.results...)
}

// Process scans for FT8 signals. Snapshot buffer under lock, heavy work outside.
func (d *FT8Detector) Process() {
	d.mu.Lock()
	if !d.enabled || d.written < int64(ft8RingSamples) {
		d.mu.Unlock()
		return
	}
	if d.linear == nil {
		d.linear = make([]float64, ft8RingSamples)
	}
	linear := d.linear
	start := int(d.written % int64(ft8RingSamples))
	copy(linear, d.audio[start:])
	copy(linear[ft8RingSamples-start:], d.audio[:start])
	d.mu.Unlock()

	var newResults []FT8Detection
	candidates := d.findCandidates(linear)
	for _, ch := range candidates {
		if det, ok := d.detectAt(linear, ch); ok {
			newResults = append(newResults, det)
		}
	}
	if len(newResults) > 5 {
		newResults = newResults[:5]
	}
	d.mu.Lock()
	d.results = newResults
	d.mu.Unlock()
}

// findCandidates uses a short FFT to find energy clusters.
func (d *FT8Detector) findCandidates(audio []float64) []float64 {
	nfft := 4096
	seg := audio[len(audio)-nfft:]
	ft8FFTRe = growF(ft8FFTRe, nfft)
	ft8FFTIm = growF(ft8FFTIm, nfft)
	re, im := ft8FFTRe[:nfft], ft8FFTIm[:nfft]
	for i := range seg {
		re[i] = seg[i]
		im[i] = 0
	}
	HannWindow(re, im)
	FFT(re, im)

	ft8Mags = growF(ft8Mags, nfft/2)[:0]
	for i := 1; i < nfft/2; i++ {
		ft8Mags = append(ft8Mags, 20*math.Log10(math.Hypot(re[i], im[i])+1e-12))
	}
	ft8SortBuf = append(ft8SortBuf[:0], ft8Mags...)
	sorted := ft8SortBuf
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	noise := sorted[len(sorted)/2]

	binHz := float64(FT8AudioRate) / float64(nfft)
	var result []float64
	inC := false
	var cStart, cPeak int
	var cPeakV float64
	for i := 1; i < nfft/2; i++ {
		hz := float64(i) * binHz
		if hz < 300 || hz > 3000 {
			continue
		}
		mag := ft8Mags[i-1]
		if mag > noise+15 {
			if !inC {
				cStart, cPeak, cPeakV = i, i, mag
				inC = true
			} else if mag > cPeakV {
				cPeak, cPeakV = i, mag
			}
		} else {
			inC = false
		}
		if !inC && cPeak > 0 {
			w := float64(i-cStart) * binHz
			if w >= 15 && w <= 300 {
				hz := math.Round(float64(cPeak)*binHz/6.25) * 6.25
				result = append(result, hz)
				if len(result) >= 3 {
					break
				}
			}
			cPeak = 0
		}
	}
	return result
}

// detectAt checks Costas sync (sliding, 4-symbol steps).
func (d *FT8Detector) detectAt(audio []float64, centerHz float64) (FT8Detection, bool) {
	step := FT8SymSamples * 4
	maxOff := len(audio) - FT8FrameSamp
	if maxOff < 0 {
		return FT8Detection{}, false
	}
	bestSync, bestSNR := 0, -999.0
	for off := 0; off <= maxOff; off += step {
		sc, snr := 0, 0.0
		for i := 0; i < 7; i++ {
			s := off + ft8SyncPositions[i]*FT8SymSamples
			if s+FT8SymSamples > len(audio) {
				break
			}
			sym := audio[s : s+FT8SymSamples]
			bt, bm, tm := 0, 0.0, 0.0
			for t := 0; t < 8; t++ {
				m := goertzelMag(sym, centerHz+(float64(t)-3.5)*FT8ToneHz, float64(FT8AudioRate))
				tm += m
				if m > bm {
					bm, bt = m, t
				}
			}
			snr += bm / (tm/8 + 1e-12)
			if bt == ft8SyncCostas[i] {
				sc++
			}
		}
		if sc > bestSync {
			bestSync, bestSNR = sc, 10*math.Log10(snr/7+1e-12)
		}
		if bestSync == 7 {
			break
		}
	}
	if bestSync < 6 {
		return FT8Detection{}, false
	}
	return FT8Detection{FreqHz: centerHz, SNRDb: bestSNR, Confidence: float64(bestSync) / 7}, true
}

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
