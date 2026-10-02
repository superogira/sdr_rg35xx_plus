package dsp

import (
	"context"
	"math"
	"math/rand"
	"os"
	"time"
)

// RunDemo drives a Chain from a synthetic signal (FM tone at center plus a
// couple of drifting carriers and a noise floor) paced at real time. It
// exists so the display and audio paths can be developed and verified with
// no dongle/server at all. getChain is consulted every block so mode
// switches mid-demo take effect.
func RunDemo(ctx context.Context, getChain func() *Chain, sink func([]float32)) {
	const blockSamples = 51200 // 25 ms at IQRate
	iq := make([]byte, 2*blockSamples)
	var audio []float32
	var phaseMain, phaseA, phaseB float64
	var t int
	rng := rand.New(rand.NewSource(1))
	// SDR_DEMO_WEFAX=1: the main station becomes a looping HF-FAX
	// transmission (USB audio 1500-2300 Hz: start tone, phasing, a
	// test chart, stop tone) so the decoder can be checked end-to-end.
	wefaxDemo := os.Getenv("SDR_DEMO_WEFAX") != ""
	var fax []float64
	if wefaxDemo {
		fax = demoWefaxAudio()
	}
	faxPos, faxPh := 0.0, 0.0
	for ctx.Err() == nil {
		start := time.Now()
		chain := getChain()
		if chain == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		dev := chain.mode.Deviation
		for i := 0; i < blockSamples; i++ {
			t++
			// Wanted FM station: 1 kHz tone, deviation per mode.
			m := math.Sin(2 * math.Pi * 1000 * float64(t) / float64(float64(IQRate)))
			phaseMain += 2 * math.Pi * dev * 0.6 * m / float64(IQRate)
			amp := 0.42
			re := amp * math.Cos(phaseMain)
			im := amp * math.Sin(phaseMain)
			if wefaxDemo {
				// A single USB tone at the fax audio frequency: RF at
				// dial + f, so USB demod hears exactly f.
				f := fax[int(faxPos)%len(fax)]
				faxPos += float64(WefaxRate) / float64(IQRate)
				faxPh += 2 * math.Pi * f / float64(IQRate)
				re = 0.30 * math.Cos(faxPh)
				im = 0.30 * math.Sin(faxPh)
			}

			// Two drifting carriers ±60-90 kHz off center.
			fA := 65e3 + 8e3*math.Sin(2*math.Pi*0.05*float64(t)/float64(IQRate))
			fB := -82e3 + 6e3*math.Sin(2*math.Pi*0.03*float64(t)/float64(IQRate))
			phaseA += 2 * math.Pi * fA / float64(IQRate)
			phaseB += 2 * math.Pi * fB / float64(IQRate)
			re += 0.20*math.Cos(phaseA) + 0.16*math.Cos(phaseB)
			im += 0.20*math.Sin(phaseA) + 0.16*math.Sin(phaseB)

			// Receiver noise floor.
			re += rng.NormFloat64() * 0.006
			im += rng.NormFloat64() * 0.006

			iq[2*i] = byte(clampU8(re*119 + 127.5))
			iq[2*i+1] = byte(clampU8(im*119 + 127.5))
		}
		audio = audio[:0]
		chain.Process(iq, &audio)
		if sink != nil {
			sink(audio)
		}
		if d := time.Until(start.Add(25 * time.Millisecond)); d > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
		}
	}
}

func clampU8(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

// demoWefaxAudio returns a fax transmission as per-sample audio
// FREQUENCIES (Hz) at WefaxRate: start tone, phasing, a chart of
// diagonal bands + a border, stop tone, a short gap.
func demoWefaxAudio() []float64 {
	const spl = WefaxRate * 60 / WefaxLPM
	var f []float64
	tone := func(v float64, n int) {
		for i := 0; i < n; i++ {
			f = append(f, 1500+800*v)
		}
	}
	apt := func(hz float64, sec float64) {
		n := int(sec * WefaxRate)
		half := WefaxRate / (2 * hz)
		for i := 0; i < n; i++ {
			v := 0.0
			if int(float64(i)/half)%2 == 1 {
				v = 1
			}
			tone(v, 1)
		}
	}
	tone(1, WefaxRate)
	apt(300, 5)
	for l := 0; l < 30; l++ {
		tone(1, spl*5/100)
		tone(0, spl-spl*5/100)
	}
	for l := 0; l < 240; l++ {
		for i := 0; i < spl; i++ {
			x := float64(i) / spl
			v := 1.0
			if int((x*8+float64(l)/30))%2 == 0 {
				v = 0.15
			}
			if x < 0.02 || x > 0.98 || l < 4 || l > 235 {
				v = 0 // border
			}
			tone(v, 1)
		}
	}
	apt(450, 5)
	tone(1, WefaxRate*2)
	return f
}
