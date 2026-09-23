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
		// The FFT window (~0.5 s) sees only a few tones of the group,
		// so the cluster midpoint can sit a whole tone step or two off
		// the true grid centre. Sync only matches at the right shift,
		// so sweep whole 6.25 Hz steps around the estimate.
		for _, off := range []float64{0, 6.25, -6.25, 12.5, -12.5, 18.75, -18.75} {
			det, ok, at := d.detectAt(linear, ch+off)
			if !ok {
				continue
			}
			det.Message = ft8DecodeAt(linear, at, ch+off)
			newResults = append(newResults, det)
			break
		}
	}
	if len(newResults) > 5 {
		newResults = newResults[:5]
	}
	// Diagnostic logging
	var maxAmp float64
	for _, v := range linear[len(linear)-8000:] {
		if v > maxAmp {
			maxAmp = v
		}
		if -v > maxAmp {
			maxAmp = -v
		}
	}
	dStr := ""
	for _, r := range newResults {
		dStr += fmt.Sprintf(" %.0fHz/%.0fdB", r.FreqHz, r.SNRDb)
		if r.Message != nil && r.Message.Valid {
			dStr += fmt.Sprintf(" \"%s\"", r.Message.Text)
		}
	}
	fmt.Fprintf(os.Stderr, "ft8: amp=%.3f cand=%d det=%d%s\n", maxAmp, len(candidates), len(newResults), dStr)

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
				// The cluster MIDPOINT estimates the centre of the
				// 8-tone group (50 Hz wide). Using the peak bin
				// instead picks whichever single tone happens to be
				// strongest, which can land a full 6.25 Hz tone step
				// away from the true grid centre.
				mid := float64(cStart+i-1) / 2
				hz := math.Round(mid*binHz/6.25) * 6.25
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

// detectAt slides the proper 3-block Costas pattern over the buffer
// and returns the best symbol-aligned offset plus a sub-symbol
// refinement. Tone t sits at centerHz+(t-3.5)*6.25 by convention, so
// centerHz is the middle of the 8-tone group (50 Hz wide). The wide
// scans rank offsets with the first Costas block only (7 positions);
// the winner is confirmed against all 21 sync positions.
func (d *FT8Detector) detectAt(audio []float64, centerHz float64) (FT8Detection, bool, int) {
	maxOff := len(audio) - FT8FrameSamp
	if maxOff < 0 {
		return FT8Detection{}, false, 0
	}
	quickScore := func(off int) (int, float64) {
		sc, soft := 0, 0.0
		for i := 0; i < 7; i++ {
			s := off + i*FT8SymSamples
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
			if bt == ft8SyncCostas[i] {
				sc++
				soft += bm / (tm/8 + 1e-12)
			}
		}
		return sc, soft
	}
	scoreAt := func(off int) (int, float64) {
		sc, soft := 0, 0.0
		for i := 0; i < 21; i++ {
			s := off + ft8SyncPos(i)*FT8SymSamples
			if s+FT8SymSamples > len(audio) {
				break
			}
			sym := audio[s : s+FT8SymSamples]
			exp := ft8SyncCostas[i%7]
			bt, bm, tm := 0, 0.0, 0.0
			for t := 0; t < 8; t++ {
				m := goertzelMag(sym, centerHz+(float64(t)-3.5)*FT8ToneHz, float64(FT8AudioRate))
				tm += m
				if m > bm {
					bm, bt = m, t
				}
			}
			if bt == exp {
				sc++
				soft += bm / (tm/8 + 1e-12)
			}
		}
		return sc, soft
	}
	bestQ, bestQS, bestOff := 0, -1.0, 0
	for off := 0; off <= maxOff; off += FT8SymSamples {
		sc, soft := quickScore(off)
		if sc > bestQ || (sc == bestQ && soft > bestQS) {
			bestQ, bestQS, bestOff = sc, soft, off
		}
	}
	// Sub-symbol refinement in two stages (eighth-symbol, then ~1.5 ms):
	// a partial-symbol skew smears tone energy between neighbours and
	// wrecks the soft decisions — LDPC can only fix so much of that.
	for _, step := range []int{160, 20} {
		span := step * 8
		base := bestOff
		for sub := -span; sub <= span; sub += step {
			off := base + sub
			if off < 0 || off > maxOff {
				continue
			}
			sc, soft := quickScore(off)
			if sc > bestQ || (sc == bestQ && soft > bestQS) {
				bestQ, bestQS, bestOff = sc, soft, off
			}
		}
	}
	// Confirm the candidate against all three Costas blocks.
	bestSync, bestScore := scoreAt(bestOff)
	if bestSync < 13 {
		return FT8Detection{}, false, 0
	}
	return FT8Detection{
		FreqHz:     centerHz,
		SNRDb:      10 * math.Log10(bestScore/21+1e-12),
		Confidence: float64(bestSync) / 21,
	}, true, bestOff
}

// ft8DecodeAt extracts soft bit LLRs for the 58 data symbols at the
// sync'd offset and runs the LDPC/CRC decode.
func ft8DecodeAt(audio []float64, off int, centerHz float64) *FT8Message {
	if off < 0 || off+FT8FrameSamp > len(audio) {
		return nil
	}
	llr := make([]float64, 174)
	mag := make([]float64, 8)
	k := 0
	for sym := 7; sym < 72; sym++ {
		if sym >= 36 && sym < 43 { // second Costas block
			continue
		}
		s := off + sym*FT8SymSamples
		symAudio := audio[s : s+FT8SymSamples]
		for t := 0; t < 8; t++ {
			mag[t] = goertzelMag(symAudio, centerHz+(float64(t)-3.5)*FT8ToneHz, float64(FT8AudioRate))
		}
		// s2 indexed by 3-bit value; FT8GrayMap maps value → tone.
		s2 := [8]float64{}
		for j := 0; j < 8; j++ {
			s2[j] = mag[FT8GrayMap[j]]
		}
		llr[3*k] = math.Max(math.Max(s2[4], s2[5]), math.Max(s2[6], s2[7])) -
			math.Max(math.Max(s2[0], s2[1]), math.Max(s2[2], s2[3]))
		llr[3*k+1] = math.Max(math.Max(s2[2], s2[3]), math.Max(s2[6], s2[7])) -
			math.Max(math.Max(s2[0], s2[1]), math.Max(s2[4], s2[5]))
		llr[3*k+2] = math.Max(math.Max(s2[1], s2[3]), math.Max(s2[5], s2[7])) -
			math.Max(math.Max(s2[0], s2[2]), math.Max(s2[4], s2[6]))
		k++
	}
	return ft8DecodeCodeword(llr)
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
