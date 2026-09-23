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
	FT8FrameSamp  = FT8NumSymbols * FT8SymSamples  // 101120 samples
)

// ft8SyncCostas is the Costas array at sync positions.
var ft8SyncCostas = [7]int{3, 1, 4, 0, 6, 5, 2}
var ft8SyncPositions = [7]int{0, 36, 37, 38, 72, 73, 74}

// FT8Detection is one detected FT8 signal.
type FT8Detection struct {
	FreqHz     float64
	SNRDb      float64
	Confidence float64
	Message    *FT8Message
}

// FT8Detector buffers 15 s of 8 kHz audio and scans it for FT8
// transmissions. The sync search slides over the entire buffer to find
// the Costas pattern at any time offset (transmissions start at :00/:15/:30/:45).
type FT8Detector struct {
	mu      sync.Mutex
	audio   []float64
	written int64
	results []FT8Detection
	enabled bool
	linear  []float64 // reusable analysis buffer (~1 MB, avoids GC churn)

	// slotStart is the sample index where the current 15 s slot began
	// (set by the user's sync press). When set, Process skips the
	// expensive sliding search and checks only at the known offset.
	// A value of -1 means "not synced, use sliding search".
	slotStart int64
	synced    bool
}

const ft8RingSamples = 8000 * 15 // full 15-second cycle

// Package-level FFT scratch buffers (reused across Process calls).
var (
	ft8FFTRe   []float64
	ft8FFTIm   []float64
	ft8Mags    []float64
	ft8SortBuf []float64
)

func growF(s []float64, n int) []float64 {
	if cap(s) >= n {
		return s[:n]
	}
	return make([]float64, n)
}

func NewFT8Detector() *FT8Detector {
	return &FT8Detector{
		audio: make([]float64, ft8RingSamples),
	}
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

// Sync marks the END of the current FT8 transmission as heard by the
// user — the next slot starts ~2.36 s later (15 s cycle − 12.64 s
// transmission). From now on the detector checks only at the known
// offset instead of sliding across the whole buffer.
func (d *FT8Detector) Sync() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.enabled {
		return
	}
	// The user pressed when a transmission ended; the NEXT slot starts
	// 2.36 s from now. Record where that lands in our sample counter.
	gapSamples := int64(2360 * FT8AudioRate / 1000) // 2.36 s at 8 kHz
	d.slotStart = d.written + int64(gapSamples)
	d.synced = true
}

// IsSynced reports whether the user has pressed sync.
func (d *FT8Detector) IsSynced() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.synced
}

func (d *FT8Detector) Enabled() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.enabled
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

// Process scans the buffer for FT8 signals. Called every second from the
// UI loop; the scan itself takes ~50 ms on the A53.
func (d *FT8Detector) Process() {
	// Snapshot the ring buffer under the lock, then RELEASE it so
	// Feed() can keep running from the DSP goroutine while we do the
	// heavy scan (holding the mutex for the whole scan froze the radio
	// stream on the A53).
	d.mu.Lock()
	if !d.enabled || d.written < int64(ft8RingSamples) {
		d.mu.Unlock()
		return
	}
	if d.linear == nil {
		d.linear = make([]float64, ft8RingSamples)
	}
	linear := d.linear
	start := d.written % ft8RingSamples
	copy(linear, d.audio[start:])
	copy(linear[ft8RingSamples-start:], d.audio[:start])
	d.mu.Unlock()

	// All heavy work happens OUTSIDE the lock on our private copy.
	var newResults []FT8Detection

	if d.synced {
		// SYNCED MODE: we know the exact slot boundary. The slot started
		// at sample slotStart; the current buffer ends at written. The
		// offset into our linear buffer is (slotStart mod ringSize),
		// adjusted for the copy rotation. After each 15 s cycle the
		// slotStart advances by 15×8000 samples.
		cycleSamples := int64(15 * FT8AudioRate)
		for d.slotStart+int64(FT8FrameSamp) > d.written {
			// Slot hasn't fully arrived yet — wait
			break
		}
		// Check if the latest complete slot is ready
		latestSlot := d.slotStart
		for latestSlot+cycleSamples+int64(FT8FrameSamp) <= d.written {
			latestSlot += cycleSamples
		}
		if latestSlot >= 0 && latestSlot+int64(FT8FrameSamp) <= d.written {
			// Compute offset in the linear buffer
			offsetInRing := int((latestSlot - (d.written - int64(ft8RingSamples) + int64(ft8RingSamples))) % int64(ft8RingSamples))
			if offsetInRing < 0 {
				offsetInRing += ft8RingSamples
			}
			// Only decode if the full frame fits in our buffer
			if offsetInRing+FT8FrameSamp <= ft8RingSamples {
				candidates := d.findCandidates(linear[offsetInRing : offsetInRing+FT8FrameSamp])
				for _, centerHz := range candidates {
					if det, ok := d.detectAtSynced(linear, offsetInRing, centerHz); ok {
						det.Message = DecodeFT8At(linear, offsetInRing, centerHz)
						newResults = append(newResults, det.FT8Detection)
					}
				}
			}
		}
	} else {
		// UNSYNCED: fall back to the full sliding search (expensive).
		candidates := d.findCandidates(linear)
		for _, centerHz := range candidates {
			if det, ok := d.detectAt(linear, centerHz); ok {
				det.Message = DecodeFT8At(linear, det.syncOffset, centerHz)
				newResults = append(newResults, det.FT8Detection)
			}
		}
	}

	if len(newResults) > 5 {
		newResults = newResults[:5]
	}

	d.mu.Lock()
	d.results = newResults
	d.mu.Unlock()
}

// detectAtSynced checks for FT8 sync at a KNOWN offset (no sliding) —
// dramatically cheaper than the full search.
func (d *FT8Detector) detectAtSynced(audio []float64, offset int, centerHz float64) (ft8DetInternal, bool) {
	if offset+FT8FrameSamp > len(audio) {
		return ft8DetInternal{}, false
	}

	syncCount := 0
	var snrSum float64

	for i := 0; i < 7; i++ {
		symStart := offset + ft8SyncPositions[i]*FT8SymSamples
		if symStart+FT8SymSamples > len(audio) {
			break
		}
		sym := audio[symStart : symStart+FT8SymSamples]

		best, bestMag, totalMag := 0, 0.0, 0.0
		for tone := 0; tone < 8; tone++ {
			toneHz := centerHz + (float64(tone)-3.5)*FT8ToneHz
			m := goertzelMag(sym, toneHz, float64(FT8AudioRate))
			totalMag += m
			if m > bestMag {
				bestMag = m
				best = tone
			}
		}
		snrSum += bestMag / (totalMag/8 + 1e-12)

		if best == ft8SyncCostas[i] {
			syncCount++
		}
	}

	if syncCount < 6 {
		return ft8DetInternal{}, false
	}

	return ft8DetInternal{
		FT8Detection: FT8Detection{
			FreqHz:     centerHz,
			SNRDb:      10 * math.Log10(snrSum/7+1e-12),
			Confidence: float64(syncCount) / 7.0,
		},
		syncOffset: offset,
	}, true
}

type ft8DetInternal struct {
	FT8Detection
	syncOffset int // sample offset where sync was found
}

// findCandidates returns candidate center frequencies with FT8-like
// energy. Scans 400-2800 Hz in 6.25 Hz steps by looking for energy in
// 50 Hz-wide clusters.
func (d *FT8Detector) findCandidates(audio []float64) []float64 {
	// Use a short FFT over 512 ms (4096 samples) to find energy clusters.
	// Buffers are package-level and reused (the A53 chokes on repeated
	// 64 KB allocations every second).
	nfft := 4096
	seg := audio[len(audio)-nfft:]
	ft8FFTRe = growF(ft8FFTRe, nfft)
	ft8FFTIm = growF(ft8FFTIm, nfft)
	re := ft8FFTRe[:nfft]
	im := ft8FFTIm[:nfft]
	for i := range seg {
		re[i] = seg[i]
		im[i] = 0
	}
	HannWindow(re, im)
	FFT(re, im)

	// Find frequency bins with energy above threshold, group into
	// clusters, return cluster centers.
	binHz := float64(FT8AudioRate) / float64(nfft) // ~1.95 Hz
	threshold := 15.0                              // dB above noise
	type cluster struct {
		start, end int
		peakBin    int
		peakVal    float64
	}
	var clusters []cluster
	inCluster := false
	noiseFloor := 0.0
	// Estimate noise floor as median — reusable buffers.
	ft8Mags = growF(ft8Mags, nfft/2)[:0]
	for i := 1; i < nfft/2; i++ {
		ft8Mags = append(ft8Mags, 20*math.Log10(math.Hypot(re[i], im[i])+1e-12))
	}
	sorted := append(ft8SortBuf[:0], ft8Mags...)
	ft8SortBuf = sorted
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	noiseFloor = sorted[len(sorted)/2]

	for i := 1; i < nfft/2; i++ {
		hz := float64(i) * binHz
		if hz < 300 || hz > 3000 {
			continue
		}
		mag := 20 * math.Log10(math.Hypot(re[i], im[i])+1e-12)
		if mag > noiseFloor+threshold {
			if !inCluster {
				clusters = append(clusters, cluster{start: i, end: i, peakBin: i, peakVal: mag})
				inCluster = true
			} else {
				clusters[len(clusters)-1].end = i
				if mag > clusters[len(clusters)-1].peakVal {
					clusters[len(clusters)-1].peakBin = i
					clusters[len(clusters)-1].peakVal = mag
				}
			}
		} else {
			inCluster = false
		}
	}

	var result []float64
	for _, c := range clusters {
		// Only interested in clusters 25-200 Hz wide (FT8 signal width)
		width := float64(c.end-c.start) * binHz
		if width < 15 || width > 300 {
			continue
		}
		hz := float64(c.peakBin) * binHz
		// Snap to 6.25 Hz grid
		hz = math.Round(hz/6.25) * 6.25
		result = append(result, hz)
		if len(result) >= 3 {
			break // at most 3 candidates — Goertzel scan is expensive
		}
	}
	return result
}

// detectAt searches for FT8 sync at the given center frequency. Slides a
// window across the buffer to find where the Costas pattern starts.
func (d *FT8Detector) detectAt(audio []float64, centerHz float64) (ft8DetInternal, bool) {
	// Try sync at multiple time offsets: scan in 128-sample steps over
	// the first 3 seconds (the sync region is at the start of the 12.64s
	// transmission, which could be anywhere in our 15s buffer).
	step := FT8SymSamples * 4 // every 4th symbol — 4x faster, still finds sync
	bestSync := 0
	bestOffset := 0
	bestSNR := -999.0

	// We need at least FT8FrameSamp samples from any candidate offset.
	maxOffset := len(audio) - FT8FrameSamp
	if maxOffset < 0 {
		return ft8DetInternal{}, false
	}

	for offset := 0; offset <= maxOffset; offset += step {
		syncCount := 0
		var snrSum float64

		for i := 0; i < 7; i++ {
			symStart := offset + ft8SyncPositions[i]*FT8SymSamples
			if symStart+FT8SymSamples > len(audio) {
				break
			}
			sym := audio[symStart : symStart+FT8SymSamples]

			// Find strongest tone
			best, bestMag, totalMag := 0, 0.0, 0.0
			for tone := 0; tone < 8; tone++ {
				toneHz := centerHz + (float64(tone)-3.5)*FT8ToneHz
				m := goertzelMag(sym, toneHz, float64(FT8AudioRate))
				totalMag += m
				if m > bestMag {
					bestMag = m
					best = tone
				}
			}
			snrSum += bestMag / (totalMag/8 + 1e-12)

			if best == ft8SyncCostas[i] {
				syncCount++
			}
		}

		if syncCount > bestSync {
			bestSync = syncCount
			bestOffset = offset
			bestSNR = 10 * math.Log10(snrSum/7+1e-12)
		}
		// Early exit on perfect match
		if syncCount == 7 {
			break
		}
	}

	if bestSync < 6 {
		return ft8DetInternal{}, false
	}

	return ft8DetInternal{
		FT8Detection: FT8Detection{
			FreqHz:     centerHz,
			SNRDb:      bestSNR,
			Confidence: float64(bestSync) / 7.0,
		},
		syncOffset: bestOffset,
	}, true
}

// DecodeFT8At decodes the FT8 message starting at the given sync offset.
func DecodeFT8At(audio []float64, syncOffset int, centerHz float64) *FT8Message {
	if syncOffset+FT8FrameSamp > len(audio) {
		return nil
	}
	frame := audio[syncOffset : syncOffset+FT8FrameSamp]

	// Extract all 79 tone indices
	tones := make([]int, 79)
	for sym := 0; sym < 79; sym++ {
		start := sym * FT8SymSamples
		samples := frame[start : start+FT8SymSamples]
		best, bestMag := 0, -1.0
		for tone := 0; tone < 8; tone++ {
			toneHz := centerHz + (float64(tone)-3.5)*FT8ToneHz
			m := goertzelMag(samples, toneHz, float64(FT8AudioRate))
			if m > bestMag {
				bestMag = m
				best = tone
			}
		}
		tones[sym] = best
	}

	// Verify sync
	syncOK := 0
	for i, expected := range ft8SyncCostas {
		if tones[ft8SyncPositions[i]] == expected {
			syncOK++
		}
	}
	if syncOK < 6 {
		return nil
	}

	// Extract codeword bits
	bitFloats := ft8ExtractSymbols(tones)
	if bitFloats == nil {
		return nil
	}

	// Convert to LLR
	llr := make([]float64, len(bitFloats))
	for i, b := range bitFloats {
		if b > 0.5 {
			llr[i] = 2.0
		} else {
			llr[i] = -2.0
		}
	}

	// LDPC decode
	decoded, _ := ldpcDecode(llr, 20)

	// Unpack message
	msg := DecodeFT8Message(decoded)
	if !msg.Valid {
		return nil
	}
	return &msg
}

// Goertzel exposes goertzelMag for external tools.
func Goertzel(samples []float64, freqHz, sampleRate float64) float64 {
	return goertzelMag(samples, freqHz, sampleRate)
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
