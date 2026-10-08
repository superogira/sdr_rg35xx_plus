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

// TestHead2HeadFiles writes deterministic 15 s slots (2 s noise lead +
// a 3-signal FT8 frame + noise tail) at several noise levels so the
// built-in decoder and the ft8ts sidecar can be compared on IDENTICAL
// audio (the node side reads the same .raw files). Also times the
// built-in Process() per slot.
func TestHead2HeadFiles(t *testing.T) {
	dir := filepath.Join("..", "..", ".webtest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Skip(err)
	}
	msgs := [][3]string{
		{"CQ", "HS0ZKO", "OK04"},
		{"CQ", "DL8YHR", "JO41"},
		{"JA1ABC", "HS0ZKO", "PM95"},
	}
	freqs := []float64{700, 1300, 2100}
	for _, ns := range []float64{0.15, 0.3, 0.5, 0.8} {
		rng := rand.New(rand.NewSource(42))
		lead := make([]float64, 2*FT8AudioRate)
		for i := range lead {
			lead[i] = ns * rng.NormFloat64()
		}
		frame := make([]float64, FT8FrameSamp)
		for k, m := range msgs {
			tones := encodeTones(pack77(m[0], m[1], m[2]))
			// in-frame noise floor: a perfectly clean frame saturates
			// the uint8 waterfall scale and the BP/OSD stage stalls on
			// uniform LLRs — real receivers always carry some noise
			one := synthFrame(tones, freqs[k], 0.45, 0.15, rng)
			for i := range frame {
				frame[i] += one[i]
			}
		}
		tailN := 15*FT8AudioRate - len(lead) - len(frame)
		tail := make([]float64, tailN)
		for i := range tail {
			tail[i] = ns * rng.NormFloat64()
		}
		all := append(append(lead, frame...), tail...)

		name := filepath.Join(dir, h2hName(ns))
		f, err := os.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		for _, v := range all {
			binary.LittleEndian.PutUint32(buf, mathFloat32bits(float32(v)))
			f.Write(buf)
		}
		f.Close()

		// built-in decoder on the same audio, several tunings
		for _, tun := range [][3]int{{7, 220, 10}, {5, 220, 10}, {4, 300, 16}, {3, 400, 24}} {
			d := NewFT8Detector()
			d.SetFT8Tuning(tun[0], tun[1], tun[2])
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
			got := map[string]bool{}
			for _, m := range d.TakeMessages() {
				if m.Valid {
					got[m.Text] = true
				}
			}
			t.Logf("builtin noise=%.2f score<=%d cap=%d budget=%d decoded=%d/3 (%.2fs)", ns, tun[0], tun[1], tun[2], len(got), dt.Seconds())
		}
	}
}

func h2hName(ns float64) string {
	switch ns {
	case 0.1:
		return "h2h_n01.raw"
	case 0.3:
		return "h2h_n03.raw"
	case 0.5:
		return "h2h_n05.raw"
	case 0.8:
		return "h2h_n08.raw"
	default:
		return "h2h_n12.raw"
	}
}

func mathFloat32bits(f float32) uint32 { return math.Float32bits(f) }
