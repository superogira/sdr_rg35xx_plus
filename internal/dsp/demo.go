package dsp

import (
	"context"
	"math"
	"math/rand"
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
			m := math.Sin(2 * math.Pi * 1000 * float64(t) / IQRate)
			phaseMain += 2 * math.Pi * dev * 0.6 * m / IQRate
			amp := 0.42
			re := amp * math.Cos(phaseMain)
			im := amp * math.Sin(phaseMain)

			// Two drifting carriers ±60-90 kHz off center.
			fA := 65e3 + 8e3*math.Sin(2*math.Pi*0.05*float64(t)/IQRate)
			fB := -82e3 + 6e3*math.Sin(2*math.Pi*0.03*float64(t)/IQRate)
			phaseA += 2 * math.Pi * fA / IQRate
			phaseB += 2 * math.Pi * fB / IQRate
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
