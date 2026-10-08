package dsp

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestH2HSweep(t *testing.T) {
	specs := []struct {
		c1, c2, g string
		f         float64
	}{
		{"CQ", "HS0ZKO", "OK04", 700},
		{"CQ", "DL8YHR", "JO41", 1300},
		{"JA1ABC", "HS0ZKO", "PM95", 2100},
	}
	for _, amp := range []float64{0.45, 0.9} {
		for _, leadSec := range []float64{0.256, 2.0} {
			rng := rand.New(rand.NewSource(42))
			inN := amp / 3.0
			frame := make([]float64, FT8FrameSamp)
			for _, sp := range specs {
				one := synthFrame(encodeTones(pack77(sp.c1, sp.c2, sp.g)), sp.f, amp, inN, rng)
				for i := range frame {
					frame[i] += one[i]
				}
			}
			lead := make([]float64, int(leadSec*FT8AudioRate))
			for i := range lead {
				lead[i] = inN * rng.NormFloat64()
			}
			all := append(append(lead, frame...), make([]float64, 2*FT8SymSamples)...)
			d := NewFT8Detector()
			d.SetEnabled(true)
			for i := 0; i < len(all); i += 512 {
				e := i + 512
				if e > len(all) {
					e = len(all)
				}
				d.Feed(all[i:e])
			}
			d.Process()
			got := 0
			for _, r := range d.Results() {
				if r.Message != nil && r.Message.Valid {
					got++
				}
			}
			t.Logf("amp=%.2f lead=%.3fs decoded=%d/3", amp, leadSec, got)
		}
	}
}

func TestH2HOSDOrder(t *testing.T) {
	for _, ns := range []float64{0.15, 0.5} {
		raw, err := os.ReadFile(filepath.Join("..", "..", ".webtest", h2hName(ns)))
		if err != nil {
			t.Skip("h2h files missing")
		}
		all := make([]float64, len(raw)/4)
		for i := range all {
			all[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		}
		for _, ord := range []int{8, 10, 12} {
			for _, ms := range []int{7, 5} {
				SetFT8OSDOrder(ord)
				d := NewFT8Detector()
				d.SetFT8Tuning(ms, 300, 24)
				d.SetEnabled(true)
				for i := 0; i < len(all); i += 512 {
					e := i + 512
					if e > len(all) {
						e = len(all)
					}
					d.Feed(all[i:e])
				}
				t0 := time.Now()
				d.Process()
				dt := time.Since(t0)
				got := 0
				for _, m := range d.TakeMessages() {
					if m.Valid {
						got++
					}
				}
				t.Logf("noise=%.2f osd=%d minScore=%d decoded=%d/3 (%.2fs)", ns, ord, ms, got, dt.Seconds())
			}
		}
		SetFT8OSDOrder(8)
	}
}
