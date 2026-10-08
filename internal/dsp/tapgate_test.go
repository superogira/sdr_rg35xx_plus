// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"math"
	"testing"
)

// TestMonitorTapsFlowWithoutDetectors: the raw 8 kHz monitor taps
// (deepcw, ft8ts sidecars) must receive audio with NO classic detector
// attached and in both the SSB-8k path and the monitor path. Twice the
// tap was nested behind a detector's nil check, silently starving the
// sidecar ("alt engine decodes nothing" in the field).
func TestMonitorTapsFlowWithoutDetectors(t *testing.T) {
	for _, mode := range []Mode{ModeUSB, ModeNFM} {
		SetIQRate(2_048_000)
		c := NewChain(mode, nil, nil)
		got := 0
		c.SetFT8TSTap(func(x []float64) { got += len(x) })
		// a tone inside the FT8 band so the branch has signal
		buf := make([]byte, 2*65536)
		phase := 0.0
		for i := 0; i < len(buf)/2; i++ {
			phase += 2 * 3.141592653589793 * 1000 / 2_048_000
			buf[2*i] = byte(127.5 + 60*cosApprox(phase))
			buf[2*i+1] = byte(127.5 + 60*sinApprox(phase))
		}
		var out []float32
		for i := 0; i < 8; i++ {
			c.Process(buf, &out)
		}
		if got == 0 {
			t.Fatalf("%s: f8ts tap got no samples with all detectors off", mode.Name)
		}
	}
}

func cosApprox(x float64) float64 { return math.Cos(x) }
func sinApprox(x float64) float64 { return math.Sin(x) }
