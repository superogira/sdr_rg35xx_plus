package dsp

import "math"

// Full-message STFT waterfall for FT8, ported from kgoba/ft8_lib's
// monitor.c/decode.c. This replaces the earlier snapshot-FFT candidate
// finder + hard-tone sync: candidates now come from soft Costas
// correlation across the whole 12.6 s message, which sees signals
// roughly 10 dB weaker than an energy threshold.

const (
	ft8WFTimeOsr = 2 // symbol subdivisions
	ft8WFFreqOsr = 2 // bin subdivisions
	ft8WFNFFT    = 2560
	ft8WFSubStep = FT8SymSamples / ft8WFTimeOsr // 640
	ft8WFMinBin  = 32                           // 200 Hz  / 6.25
	ft8WFMaxBin  = 481                          // 3000 Hz / 6.25 + 1
	ft8WFNumBins = ft8WFMaxBin - ft8WFMinBin    // 449 bins @ 6.25 Hz
	ft8WFBlocks  = 93                           // 15 s of symbols
)

// fft2560Scratch holds the mixed-radix working buffers (package-level,
// reused; the detector runs single-threaded behind the busy guard).
var fft2560Scratch struct {
	sub [5][]complex128 // five 512-point sub-FFT views
	tw  []complex128    // W_2560 twiddles per (k1, r)
}

// fft2560 computes a 2560-point DFT in place (2560 = 5 × 512; five
// power-of-two FFTs combined by a 5×5 butterfly), giving the exact
// 3.125 Hz bin grid the FT8 tone spacing needs.
func fft2560(re, im []float64) {
	const N = ft8WFNFFT
	const N1 = 512 // sub-FFT length (power of two)
	const N2 = 5    // number of sub-FFTs
	for r := 0; r < N2; r++ {
		if cap(fft2560Scratch.sub[r]) < N1 {
			fft2560Scratch.sub[r] = make([]complex128, N1)
		}
		sub := fft2560Scratch.sub[r][:N1]
		for n := 0; n < N1; n++ {
			sub[n] = complex(re[n*N2+r], im[n*N2+r])
		}
		subRe := make([]float64, N1)
		subIm := make([]float64, N1)
		for n := 0; n < N1; n++ {
			subRe[n], subIm[n] = real(sub[n]), imag(sub[n])
		}
		FFT(subRe, subIm)
		for n := 0; n < N1; n++ {
			sub[n] = complex(subRe[n], subIm[n])
		}
		_ = sub
	}
	// combine: X[k1 + 512*k2] = Σ_r Y_r[k1] · W_N^{r·k1} · W_5^{r·k2}
	if len(fft2560Scratch.tw) != N2*N1 {
		fft2560Scratch.tw = make([]complex128, N2*N1)
		for r := 0; r < N2; r++ {
			for k1 := 0; k1 < N1; k1++ {
				a := -2 * math.Pi * float64(r*k1) / float64(N)
				fft2560Scratch.tw[r*N1+k1] = complex(math.Cos(a), math.Sin(a))
			}
		}
	}
	var w5 [5]complex128
	for r := 0; r < N2; r++ {
		a := -2 * math.Pi * float64(r) / float64(N2)
		w5[r] = complex(math.Cos(a), math.Sin(a))
	}
	out := make([]complex128, N)
	for k1 := 0; k1 < N1; k1++ {
		for k2 := 0; k2 < N2; k2++ {
			acc := complex(0, 0)
			for r := 0; r < N2; r++ {
				y := fft2560Scratch.sub[r][k1]
				acc += y * fft2560Scratch.tw[r*N1+k1] * w5[(r*k2)%N2]
			}
			out[k1+N1*k2] = acc
		}
	}
	for i := 0; i < N; i++ {
		re[i], im[i] = real(out[i]), imag(out[i])
	}
}

// ft8Waterfall is the incremental STFT ring (93 symbols deep).
type ft8Waterfall struct {
	window    []float64 // Hann × 2/nfft
	lastFrame []float64 // sliding 2560-sample analysis frame
	mag       []uint8   // blocks × timeOsr × freqOsr × numBins (0.5 dB steps, -120..0 dB)
	head      int       // next block write index (circular)
	count     int       // blocks stored (≤ ft8WFBlocks)
	frameRe   []float64
	frameIm   []float64
}

func newFT8Waterfall() *ft8Waterfall {
	w := &ft8Waterfall{
		window:    make([]float64, ft8WFNFFT),
		lastFrame: make([]float64, ft8WFNFFT),
		mag:       make([]uint8, ft8WFBlocks*ft8WFTimeOsr*ft8WFFreqOsr*ft8WFNumBins),
		frameRe:   make([]float64, ft8WFNFFT),
		frameIm:   make([]float64, ft8WFNFFT),
	}
	for i := range w.window {
		x := math.Sin(math.Pi * float64(i) / float64(ft8WFNFFT))
		w.window[i] = x * x * (2.0 / float64(ft8WFNFFT))
	}
	return w
}

func (w *ft8Waterfall) reset() {
	w.head, w.count = 0, 0
	for i := range w.lastFrame {
		w.lastFrame[i] = 0
	}
}

// feed consumes samples, computing one waterfall block per 640.
// feed consumes whole symbols (FT8SymSamples each); sub-symbol
// remainders stay in the caller's pending buffer.
func (w *ft8Waterfall) feed(samples []float64) {
	for len(samples) >= FT8SymSamples {
		w.processBlock(samples[:FT8SymSamples])
		samples = samples[FT8SymSamples:]
	}
}

func (w *ft8Waterfall) symbolStride() int { return ft8WFTimeOsr * ft8WFFreqOsr * ft8WFNumBins }

// blockBase returns the mag slice base for absolute block index b
// (0 = oldest stored block).
func (w *ft8Waterfall) blockBase(b int) int {
	phys := b
	if w.count == ft8WFBlocks {
		phys = (w.head + b) % ft8WFBlocks
	}
	return phys * w.symbolStride()
}

// processBlock consumes ONE symbol's samples (FT8SymSamples) and
// computes the timeOsr sub-blocks for it, each shifted by
// ft8WFSubStep like ft8_lib's monitor_process (the frame argument is
// consumed sequentially — 640 samples per timeSub).
func (w *ft8Waterfall) processBlock(frame []float64) {
	base := w.head * w.symbolStride()
	pos := 0
	for timeSub := 0; timeSub < ft8WFTimeOsr; timeSub++ {
		copy(w.lastFrame, w.lastFrame[ft8WFSubStep:])
		copy(w.lastFrame[ft8WFNFFT-ft8WFSubStep:], frame[pos:pos+ft8WFSubStep])
		pos += ft8WFSubStep
		for i := 0; i < ft8WFNFFT; i++ {
			w.frameRe[i] = w.window[i] * w.lastFrame[i]
			w.frameIm[i] = 0
		}
		fft2560(w.frameRe, w.frameIm)
		off := base + timeSub*ft8WFFreqOsr*ft8WFNumBins
		for freqSub := 0; freqSub < ft8WFFreqOsr; freqSub++ {
			for bin := 0; bin < ft8WFNumBins; bin++ {
				src := (ft8WFMinBin+bin)*ft8WFFreqOsr + freqSub
				mag2 := w.frameRe[src]*w.frameRe[src] + w.frameIm[src]*w.frameIm[src]
				db := 10 * math.Log10(1e-12+mag2)
				v := int(2*db + 240)
				if v < 0 {
					v = 0
				} else if v > 255 {
					v = 255
				}
				w.mag[off] = uint8(v)
				off++
			}
		}
	}
	w.head = (w.head + 1) % ft8WFBlocks
	if w.count < ft8WFBlocks {
		w.count++
	}
}

// ft8WFCand is one candidate from the Costas correlation search.
type ft8WFCand struct {
	timeOff int // symbol index of the message start (from oldest block)
	timeSub int
	freqOff int // bin index of tone 0
	freqSub int
	score   int
}

// syncScore is ft8_lib's neighbour-difference Costas score.
func (w *ft8Waterfall) syncScore(c ft8WFCand) int {
	score, numAvg := 0, 0
	for m := 0; m < 3; m++ {
		for k := 0; k < 7; k++ {
			blockAbs := c.timeOff + 36*m + k
			if blockAbs < 0 || blockAbs >= w.count {
				continue
			}
			p8 := w.mag[w.blockBase(blockAbs)+c.timeSub*ft8WFFreqOsr*ft8WFNumBins+c.freqSub*ft8WFNumBins+c.freqOff:]
			sm := ft8SyncCostas[k]
			if sm > 0 {
				score += int(p8[sm]) - int(p8[sm-1])
				numAvg++
			}
			if sm < 7 {
				score += int(p8[sm]) - int(p8[sm+1])
				numAvg++
			}
			if k > 0 && blockAbs > 0 {
				prev := w.mag[w.blockBase(blockAbs-1)+c.timeSub*ft8WFFreqOsr*ft8WFNumBins+c.freqSub*ft8WFNumBins+c.freqOff:]
				score += int(p8[sm]) - int(prev[sm])
				numAvg++
			}
			if k+1 < 7 && blockAbs+1 < w.count {
				next := w.mag[w.blockBase(blockAbs+1)+c.timeSub*ft8WFFreqOsr*ft8WFNumBins+c.freqSub*ft8WFNumBins+c.freqOff:]
				score += int(p8[sm]) - int(next[sm])
				numAvg++
			}
		}
	}
	if numAvg > 0 {
		score /= numAvg
	}
	return score
}

// findCandidates searches every (time, freq) cell for the Costas
// pattern and keeps the best-scoring ones.
func (w *ft8Waterfall) findCandidates(maxCand, minScore int) []ft8WFCand {
	var best []ft8WFCand
	worst := minScore
	for timeSub := 0; timeSub < ft8WFTimeOsr; timeSub++ {
		for freqSub := 0; freqSub < ft8WFFreqOsr; freqSub++ {
			for timeOff := 0; timeOff+79 <= w.count; timeOff++ {
				for freqOff := 0; freqOff+8 <= ft8WFNumBins; freqOff++ {
					c := ft8WFCand{timeOff: timeOff, timeSub: timeSub, freqOff: freqOff, freqSub: freqSub}
					s := w.syncScore(c)
					if s < worst {
						continue
					}
					c.score = s
					if len(best) < maxCand {
						best = append(best, c)
						if len(best) == maxCand {
							worst = minInt(worst, minScoreOf(best))
						}
						continue
					}
					// replace the weakest
					wi := 0
					for i, b := range best {
						if b.score < best[wi].score {
							wi = i
						}
					}
					if c.score > best[wi].score {
						best[wi] = c
					}
				}
			}
		}
	}
	// Strongest first: the consumer spends its decode budget in this
	// order, so weak-score noise cells (admitted by a low threshold)
	// can never crowd out real signals.
	for i := 1; i < len(best); i++ {
		for j := i; j > 0 && best[j].score > best[j-1].score; j-- {
			best[j], best[j-1] = best[j-1], best[j]
		}
	}
	return best
}

func minScoreOf(cs []ft8WFCand) int {
	m := cs[0].score
	for _, c := range cs[1:] {
		if c.score < m {
			m = c.score
		}
	}
	return m
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// extractLLR fills 174 soft LLRs from the waterfall for a candidate
// (ft8_lib ft8_extract_likelihood + ft8_extract_symbol).
// extractLLR fills 174 soft LLRs for a candidate. The candidate's
// timeSub marks the sub-symbol start, but the 2560-sample analysis
// frame needs a full symbol of lead-in: when timeSub is 1, the first
// fully-observable data symbol lives one block later.
//
// Each symbol's energy appears in BOTH time-sub rows of its block —
// the ts=0 row centres on the symbol, the ts=1 row straddles it and
// the next — so the soft values average the two rows' linear powers
// (full weight ts=0, half weight ts=1) for ~1 dB of diversity.
func (w *ft8Waterfall) extractLLR(c ft8WFCand, llr []float64) {
	ts := c.timeSub
	timeOff := c.timeOff
	if ts == 1 {
		timeOff++
		ts = 0
	}
	subOff := ts*ft8WFFreqOsr*ft8WFNumBins + c.freqSub*ft8WFNumBins + c.freqOff
	k := 0
	for sym := 0; sym < 58; sym++ {
		symIdx := sym
		if sym < 29 {
			symIdx += 7
		} else {
			symIdx += 14
		}
		blockAbs := timeOff + symIdx
		if blockAbs < 0 || blockAbs >= w.count {
			llr[3*sym], llr[3*sym+1], llr[3*sym+2] = 0, 0, 0
			continue
		}
		// (Averaging the block's second time-sub row was tried and
		// rejected: it straddles the NEXT symbol's tone, injecting more
		// interference than the extra half-row buys — no floor gain at
		// quarter weight, regression at half.)
		bins := w.mag[w.blockBase(blockAbs)+subOff:]
		var s2 [8]float64
		for j := 0; j < 8; j++ {
			s2[j] = float64(bins[FT8GrayMap[j]])
		}
		llr[3*k] = math.Max(math.Max(s2[4], s2[5]), math.Max(s2[6], s2[7])) -
			math.Max(math.Max(s2[0], s2[1]), math.Max(s2[2], s2[3]))
		llr[3*k+1] = math.Max(math.Max(s2[2], s2[3]), math.Max(s2[6], s2[7])) -
			math.Max(math.Max(s2[0], s2[1]), math.Max(s2[4], s2[5]))
		llr[3*k+2] = math.Max(math.Max(s2[1], s2[3]), math.Max(s2[5], s2[7])) -
			math.Max(math.Max(s2[0], s2[2]), math.Max(s2[4], s2[6]))
		k++
	}
}

// candFreqHz converts a candidate's bin position to Hz.
func (c ft8WFCand) candFreqHz() float64 {
	return float64((ft8WFMinBin+c.freqOff)*ft8WFFreqOsr+c.freqSub) * (float64(FT8AudioRate) / float64(ft8WFNFFT))
}

// candSNRDb estimates SNR in the FT8-standard convention: dB of tone
// power above noise in a 2500 Hz reference band (the scale WSJT-X and
// pskreporter display). The waterfall stores 0.5 dB steps as
// v = 2·dB + 240 → dB(v) = (v−240)/2, linear power 10^dB/10.
//
// Noise comes from bins OUTSIDE the tone group: the 2-symbol analysis
// window smears each symbol's tone across the neighbouring rows, so
// the other 7 bins of the tone row itself carry adjacent-symbol
// energy — measuring them (as the first cut did) yielded a nearly
// constant ~8 dB regardless of actual SNR. Offsets 9-16 bins each way
// sit 28-50 Hz out, past the leakage skirt but inside the usual gap
// to neighbouring signals.
func (w *ft8Waterfall) candSNRDb(c ft8WFCand) float64 {
	binHz := float64(FT8AudioRate) / float64(ft8WFNFFT)
	refBand := 10 * math.Log10(2500.0/binHz) // 3.125 Hz → 2500 Hz ≈ 29 dB
	noiseOffs := []int{-18, -16, -14, -12, -11, 15, 16, 17, 19, 21}
	var sumRatio float64
	n := 0
	for m := 0; m < 3; m++ {
		for k := 0; k < 7; k++ {
			blockAbs := c.timeOff + 36*m + k
			if blockAbs < 0 || blockAbs >= w.count {
				continue
			}
			row := w.blockBase(blockAbs) + c.timeSub*ft8WFFreqOsr*ft8WFNumBins + c.freqSub*ft8WFNumBins + c.freqOff
			sm := ft8SyncCostas[k]
			toneP := math.Pow(10, (float64(w.mag[row+sm])-240)/20)
			var noiseP float64
			nc := 0
			for _, off := range noiseOffs {
				b := c.freqOff + off
				if b < 0 || b+8 > ft8WFNumBins {
					continue
				}
				noiseP += math.Pow(10, (float64(w.mag[row+off])-240)/20)
				nc++
			}
			if nc == 0 || noiseP <= 0 {
				continue
			}
			sumRatio += toneP / (noiseP / float64(nc))
			n++
		}
	}
	if n == 0 {
		return 0
	}
	snr := 10*math.Log10(sumRatio/float64(n)) - refBand
	if snr > 40 {
		snr = 40
	}
	if snr < -40 {
		snr = -40
	}
	return snr
}
