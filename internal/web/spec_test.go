// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package web

import (
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"

	"sdr35/internal/dsp"
	"sdr35/internal/radio"
)

// The waterfall floor EMA must stay in dB. An earlier version tracked
// the 25th percentile in linear power: after FFT amplification that
// floor lands in the hundreds/thousands while the per-bin metric is a
// ~60 dB number, so the subtraction went negative for every bin and
// the whole web waterfall faded to black a few dozen polls after
// startup (the EMA climbs from 0, masking the bug on a fresh process).
func TestSpecFloorStaysLiveAfterConvergence(t *testing.T) {
	r := radio.New("127.0.0.1:1234", 100_000_000, dsp.ModeAM, 20, nil)
	s := &Server{radio: r}

	// Deterministic noise floor plus a strong tone a quarter-span up.
	rng := rand.New(rand.NewSource(1))
	toneHz := 0.25 * float64(dsp.IQRate)
	block := make([]complex128, 4096)
	for i := range block {
		ph := 2 * math.Pi * toneHz * float64(i%4096) / float64(dsp.IQRate)
		block[i] = complex(0.2*rng.NormFloat64()+0.5*math.Cos(ph),
			0.2*rng.NormFloat64()+0.5*math.Sin(ph))
	}

	// Push once (the tap keeps the newest history), then poll far past
	// the 0.05-gain EMA's convergence (~60 calls).
	// re-push each round so every poll sees fresh generations.
	var last spec
	for poll := 0; poll < 200; poll++ {
		for i := range block {
			ph := 2 * math.Pi * toneHz * float64(i) / float64(dsp.IQRate)
			block[i] = complex(0.2*rng.NormFloat64()+0.5*math.Cos(ph),
				0.2*rng.NormFloat64()+0.5*math.Sin(ph))
		}
		r.RawTap().Push(block)
		rec := httptest.NewRecorder()
		s.handleSpec(rec, httptest.NewRequest(http.MethodGet, "/api/spec", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("poll %d: status %d", poll, rec.Code)
		}
		if poll == 199 {
			if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
				t.Fatalf("decode spec json: %v", err)
			}
		}
	}

	mx, lit := int16(0), 0
	for _, b := range last.Bins {
		if b > mx {
			mx = b
		}
		if b > 60 {
			lit++
		}
	}
	if mx < 400 {
		t.Fatalf("tone bin too dim after floor convergence: max=%d (want ≥400 of 620)", mx)
	}
	if lit < 100 {
		t.Fatalf("only %d/8192 bins visible — floor is swallowing the display", lit)
	}
}
