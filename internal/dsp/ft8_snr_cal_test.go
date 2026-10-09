package dsp

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// TestSNRCalibrationFiles writes 15 s slots carrying ONE real FT8
// transmission at a known true SNR (2500 Hz reference-band convention:
// tone power A^2/2 in its 3.125 Hz bin vs white noise of variance s2
// whose 2500 Hz power is s2*2500/8000), then reports what the built-in
// detector estimates. The same .raw files are fed to the ft8ts bench
// decoder to compare scales.
func TestSNRCalibrationFiles(t *testing.T) {
	dir := filepath.Join("..", "..", ".webtest")
	os.MkdirAll(dir, 0o755)
	const A = 0.45
	for _, snr := range []float64{-24, -18, -12, -6, 0} {
		// sigma^2 = (A^2/2) / (0.3125 * 10^(snr/10))
		s2 := (A * A / 2) / (0.3125 * math.Pow(10, snr/10))
		sigma := math.Sqrt(s2)
		rng := rand.New(rand.NewSource(7))
		n := 15 * FT8AudioRate
		all := make([]float64, n)
		for i := range all {
			all[i] = sigma * rng.NormFloat64()
		}
		frame := synthFrame(encodeTones(pack77("CQ", "HS0ZKO", "OK04")), 1000.0, A, 0, rng)
		off := 2 * FT8SymSamples // same lead as the passing tests (ring holds 93 blocks)
		for i, v := range frame {
			all[off+i] += v
		}
		name := filepath.Join(dir, snrName(snr))
		f, _ := os.Create(name)
		buf := make([]byte, 4)
		for _, v := range all {
			binary.LittleEndian.PutUint32(buf, math.Float32bits(float32(v)))
			f.Write(buf)
		}
		f.Close()

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
		got := math.NaN()
		for _, m := range d.TakeMessages() {
			if m.Valid {
				got = m.SNRDb
			}
		}
		if math.IsNaN(got) && snr <= -18 {
			continue // below the detector's floor at this ring size
		}
		if math.IsNaN(got) || math.Abs(got-snr) > 1.0 {
			t.Errorf("true=%+.0f dB builtin=%+.1f dB — off by more than 1 dB", snr, got)
		} else {
			t.Logf("true=%+.0f dB builtin=%+.1f dB", snr, got)
		}
	}
}

func snrName(snr float64) string {
	switch int(snr) {
	case -24:
		return "snr_m24.raw"
	case -18:
		return "snr_m18.raw"
	case -12:
		return "snr_m12.raw"
	case -6:
		return "snr_m06.raw"
	default:
		return "snr_p00.raw"
	}
}
