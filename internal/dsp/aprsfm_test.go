package dsp

import (
	"math"
	"testing"

	"sdr35/internal/aprs"
)

// fmModulateAFSK builds an IQ stream of an FM carrier whose audio is
// the AFSK beacon (deviation 5 kHz, as real VHF APRS): the exact signal
// a dongle delivers on 144.390.
func fmModulateAFSK(body []byte, seconds float64, deviation, carrierHz float64) []byte {
	audio := aprs.Modulate(body, 0.6, 40)
	n := int(seconds * float64(IQRate))
	out := make([]byte, 2*n)
	ph := 0.0
	ap := 0.0
	for i := 0; i < n; i++ {
		// audio sample at IQ rate: hold each 48k sample IQRate/48000 times
		idx := i * 48000 / IQRate
		if idx >= len(audio) {
			idx = len(audio) - 1
		}
		f := carrierHz + deviation*float64(audio[idx])
		ph += 2 * math.Pi * f / float64(IQRate)
		out[2*i] = byte(math.Round(0.5*math.Cos(ph)*119 + 127.5 + 8))
		out[2*i+1] = byte(math.Round(0.5*math.Sin(ph)*119 + 127.5 + 8))
		_ = ap
	}
	return out
}

type frameSink struct {
	frames [][]byte
}

func (f *frameSink) Feed(x []float64) { _ = x }

// TestAPRSFMBranchDecodesRealSignal: the whole receive path — FM
// carrier at an offset inside the IF2 window, through the chain's
// APRS branch — must yield the original frame.
func TestAPRSFMBranchDecodesRealSignal(t *testing.T) {
	SetIQRate(1_024_000) // IF2 = 128k, decim 4 → 32k
	info := "!1357.33N/10033.71E>045/030RG35XX /A=000250"
	body := aprs.EncodeUI("HS1ABC-7", "APRS", []string{"WIDE1-1"}, []byte(info))
	iq := fmModulateAFSK(body, 1.2, 5000, 25000) // channel at +25 kHz offset

	dem := aprs.NewDemodulator()
	ch := NewChain(ModeNFM, nil, nil)
	ch.SetAPRSMonitor(dem)
	ch.SetAPRSOffset(25000)

	var audio []float32
	const size = 65536
	for off := 0; off < len(iq); off += size {
		end := off + size
		if end > len(iq) {
			end = len(iq)
		}
		ch.Process(iq[off:end], &audio)
	}
	frames := dem.TakeFrames()
	if len(frames) == 0 {
		t.Fatal("FM-branch decode produced no frames from a real FM signal")
	}
	var got *aprs.Frame
	for _, f := range frames {
		if fr := aprs.DecodeFrame(f); fr != nil && fr.Src == "HS1ABC-7" {
			got = fr
		}
	}
	if got == nil {
		t.Fatalf("%d frames, none from HS1ABC-7", len(frames))
	}
	if string(got.Info) != info {
		t.Errorf("info = %q", got.Info)
	}
}

// The branch must also work with the channel exactly at the LO
// (offset 0) — the black-spot case the user asked about.
func TestAPRSFMBranchAtLO(t *testing.T) {
	SetIQRate(1_024_000)
	info := "!1357.33N/10033.71E>"
	body := aprs.EncodeUI("E20XYZ", "APRS", nil, []byte(info))
	iq := fmModulateAFSK(body, 1.2, 5000, 0)

	dem := aprs.NewDemodulator()
	ch := NewChain(ModeNFM, nil, nil)
	ch.SetAPRSMonitor(dem)
	ch.SetAPRSOffset(0)
	var audio []float32
	for off := 0; off < len(iq); off += 65536 {
		end := off + 65536
		if end > len(iq) {
			end = len(iq)
		}
		ch.Process(iq[off:end], &audio)
	}
	frames := dem.TakeFrames()
	ok := false
	for _, f := range frames {
		if fr := aprs.DecodeFrame(f); fr != nil && fr.Src == "E20XYZ" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("no decode at LO centre (%d frames)", len(frames))
	}
}
