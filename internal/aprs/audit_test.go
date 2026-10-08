// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import (
	"math"
	"testing"
)

// The following tests audit the AFSK modulator from OUTSIDE the
// package's own demodulator: they measure the generated audio with
// independent estimators (zero crossings, windowed tone counts) and
// decode the bits with a from-scratch NRZI/HDLC reader, so a defect
// shared by modulator and demodulator cannot hide.

// goertzel magnitude at hz over seg — phase-independent, the standard
// DFT-bin estimator (independent of the package's correlators).
func goertzel(seg []float64, hz float64) float64 {
	w := 2 * math.Pi * hz / float64(TXRate)
	coeff := 2 * math.Cos(w)
	var s0, s1, s2 float64
	for _, v := range seg {
		s0 = v + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	return math.Sqrt(s1*s1 + s2*s2 - coeff*s1*s2)
}

// toneAt classifies a bit window as mark or space by Goertzel power.
func toneAt(seg []float64) float64 {
	if goertzel(seg, MarkHz) > goertzel(seg, SpaceHz) {
		return MarkHz
	}
	return SpaceHz
}

// TestModulatorTonePurity: every bit period must carry exactly one of
// the two Bell-202 tones, at full amplitude, with no DC or clipping.
func TestModulatorTonePurity(t *testing.T) {
	body, _ := buildTestFrame()
	audio := Modulate(body, 0.9, 30)
	spb := TXRate / Baud // 40 samples per bit
	// Skip the fade region and the first partial bit.
	start := 240 / spb * spb
	wrong := 0
	for i := start; i+spb <= len(audio)-4800; i += spb { // stop at the silent tail
		seg := audio[i : i+spb]
		pm, ps := goertzel(seg, MarkHz), goertzel(seg, SpaceHz)
		lo, hi := pm, ps
		if lo > hi {
			lo, hi = hi, lo
		}
		if hi < 1e-9 || lo/hi > 0.35 { // the losing tone must be far below
			wrong++
			if wrong < 4 {
				t.Logf("bit at %d: mark/space power ratio %.2f", i/spb, lo/hi)
			}
		}
	}
	if wrong > 0 {
		t.Fatalf("%d bit windows with an off tone", wrong)
	}
	peak := 0.0
	for _, v := range audio {
		if a := math.Abs(v); a > peak {
			peak = a
		}
	}
	if peak < 0.85 || peak > 1.0 {
		t.Fatalf("peak amplitude %v, want ~0.9", peak)
	}
	// Fade-in only: the first 5 ms ramps; the closing flag (last
	// non-silent bit window) must be at FULL amplitude, and the tail
	// must be silent.
	if math.Abs(audio[0]) > 0.05 {
		t.Fatal("fade-in missing")
	}
	tail := audio[len(audio)-4800:]
	for _, v := range tail {
		if v != 0 {
			t.Fatal("tail must be silent")
		}
	}
	flagStart := len(audio) - 4800 - 40*8 // last flag = 8 bits
	seg := audio[flagStart+40 : flagStart+80]
	pm, ps := goertzel(seg, MarkHz), goertzel(seg, SpaceHz)
	if math.Max(pm, ps) < 0.5*math.Max(goertzel(audio[1000:1040], MarkHz), goertzel(audio[1000:1040], SpaceHz)) {
		t.Fatal("closing flag must be at full amplitude")
	}
}

// TestModulatorBaudExact: the total bit count implied by the audio
// length must match the flag+stuffed-frame bit count exactly (±fade).
func TestModulatorBaudExact(t *testing.T) {
	body, _ := buildTestFrame()
	for _, flags := range []int{15, 40, 300} {
		bits := probeBits(body, flags)
		audio := Modulate(body, 0.8, flags)
		want := float64(len(bits))*float64(TXRate)/float64(Baud) + 4800
		if d := math.Abs(float64(len(audio)) - want); d > 2 {
			t.Fatalf("flags=%d: %d samples, want %.0f (±2)", flags, len(audio), want)
		}
	}
}

// decodeBitsIndependently reads NRZI bits straight off the audio with
// windowed tone counts (no correlators, no DPLL — the modulator is
// sample-exact so plain windows suffice), then deframes HDLC.
func decodeBitsIndependently(audio []float64, skipSamples, stopBefore int) []byte {
	spb := TXRate / Baud
	var bits []byte
	prevTone := -1
	for i := skipSamples; i+spb <= len(audio)-stopBefore; i += spb {
		f := toneAt(audio[i : i+spb])
		tone := 1
		if math.Abs(f-SpaceHz) < math.Abs(f-MarkHz) {
			tone = 0
		}
		if prevTone >= 0 {
			if tone == prevTone {
				bits = append(bits, 1)
			} else {
				bits = append(bits, 0)
			}
		}
		prevTone = tone
	}
	return bits
}

func deframeIndependently(bits []byte) []byte {
	// Shift-register flag hunt, written independently of the
	// production deframer.
	var shift uint8
	var oR int
	var f []byte
	var c byte
	var cn, st int
	inF := false
	for _, b := range bits {
		shift = shift<<1 | b
		if b == 1 {
			oR++
		} else {
			oR = 0
		}
		if shift == 0x7E {
			if inF && len(f) >= 17 && ValidFCS(f) {
				return append([]byte(nil), f[:len(f)-2]...)
			}
			inF = true
			f, c, cn, st = f[:0], 0, 0, 0
			continue
		}
		if oR > 6 {
			inF = false
			f, c, cn, st = f[:0], 0, 0, 0
			continue
		}
		if oR == 6 {
			if inF && len(f) >= 17 && ValidFCS(f) {
				return append([]byte(nil), f[:len(f)-2]...)
			}
			inF = true
			f, c, cn, st = f[:0], 0, 0, 0
			continue
		}
		if !inF {
			continue
		}
		if st == 5 && b == 0 {
			st = 0
			continue
		}
		if b == 1 {
			st++
		} else {
			st = 0
		}
		c |= b << cn
		cn++
		if cn == 8 {
			f = append(f, c)
			c, cn = 0, 0
		}
	}
	return nil
}

// TestIndependentDecode: the audio alone must carry the frame — read
// by estimators that share no code with the demodulator.
func TestIndependentDecode(t *testing.T) {
	body, info := buildTestFrame()
	audio := Modulate(body, 0.9, 40)
	// The fade covers the first ~6 bits' worth of samples; start after
	// it and let the first window settle.
	// The 240-sample fades cover exactly the first/last six bits;
	// decode strictly between them (bit-aligned windows).
	bits := decodeBitsIndependently(audio, 240, 4800)
	frame := deframeIndependently(bits)
	if frame == nil {
		t.Fatal("independent reader found no valid frame")
	}
	fr := DecodeFrame(frame)
	if fr == nil {
		t.Fatal("frame did not parse")
	}
	if fr.Src != "HS1ABC-7" || string(fr.Info) != info {
		t.Fatalf("decoded %+v info %q", fr, fr.Info)
	}
}

// TestRateRobustness: a receiver whose clock runs ±100 ppm off must
// still decode (phones resample; sound cards drift).
func TestRateRobustness(t *testing.T) {
	body, _ := buildTestFrame()
	audio := Modulate(body, 0.9, 40)
	for _, ppm := range []float64{-100, 0, 100} {
		ratio := 1 + ppm/1e6
		n := int(float64(len(audio)) * ratio)
		res := make([]float64, n)
		for i := range res {
			x := float64(i) / ratio
			i0 := int(x)
			if i0+1 >= len(audio) {
				break
			}
			f := x - float64(i0)
			res[i] = audio[i0]*(1-f) + audio[i0+1]*f
		}
		// feed through the real demod at 8k (decimate 6:1)
		fed := make([]float64, 0, len(res)/6)
		for i := 0; i < len(res); i += 6 {
			fed = append(fed, res[i])
		}
		dem := NewDemodulator()
		dem.Feed(fed)
		frames := dem.TakeFrames()
		ok := false
		for _, f := range frames {
			if fr := DecodeFrame(f); fr != nil && fr.Src == "HS1ABC-7" {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("decode failed at %+d ppm clock offset", int(ppm))
		}
	}
}

// TestPreambleDuration: the VOX preamble must span the requested time
// (flags at 1200 baud = 8 bits each = 8/1200 s).
func TestPreambleDuration(t *testing.T) {
	body, _ := buildTestFrame()
	for sec, flags := range map[float64]int{0.1: 15, 0.3: 45, 1.0: 150, 2.0: 300} {
		audio := Modulate(body, 0.8, flags)
		wantSec := float64(flags) * 8 / Baud
		// total = preamble + frame; just bound the preamble share
		if got := float64(len(audio)) / TXRate; got < wantSec {
			t.Fatalf("preamble %.1fs: audio %.3fs shorter than preamble %.3fs", sec, got, wantSec)
		}
	}
}

func TestDebugBitsDiff(t *testing.T) {
	body, _ := buildTestFrame()
	audio := Modulate(body, 0.9, 40)
	got := decodeBitsIndependently(audio, 240, 240)
	want := probeBits(body, 40)
	// got should equal want[7 : len(want)-6]
	exp := want[7 : len(want)-6]
	t.Logf("got %d bits, expected slice %d", len(got), len(exp))
	n := len(got)
	if len(exp) < n {
		n = len(exp)
	}
	first := -1
	for i := 0; i < n; i++ {
		if got[i] != exp[i] {
			first = i
			break
		}
	}
	if first >= 0 {
		lo := first - 6
		if lo < 0 {
			lo = 0
		}
		t.Fatalf("first divergence at bit %d: got=%v exp=%v", first, got[lo:first+10], exp[lo:first+10])
	}
	if n < len(exp) {
		t.Fatalf("short by %d bits", len(exp)-n)
	}
	t.Log("independent bits identical to production bit plan")
}

func TestDebugDeframeExact(t *testing.T) {
	body, _ := buildTestFrame()
	bits := probeBits(body, 40)
	f := deframeIndependently(bits)
	t.Logf("deframe on exact bits: %v", f != nil)
	if f == nil {
		t.Fatal("independent deframer fails even on exact bits")
	}
}

func TestDebugDeframeTrace(t *testing.T) {
	body, _ := buildTestFrame()
	bits := probeBits(body, 40)
	// inline trace version of the independent deframer
	var shift uint8
	var oR int
	var f []byte
	var c byte
	var cn, st int
	inF := false
	closes := 0
	for _, b := range bits {
		shift = shift<<1 | b
		if b == 1 {
			oR++
		} else {
			oR = 0
		}
		if oR > 6 {
			inF = false
			f, c, cn, st, oR = f[:0], 0, 0, 0, 0
			continue
		}
		if shift == 0x7E || oR == 6 {
			closes++
			if inF {
				t.Logf("close #%d: len(f)=%d valid=%v", closes, len(f), ValidFCS(f))
			}
			inF = true
			f, c, cn, st = f[:0], 0, 0, 0
			continue
		}
		if !inF {
			continue
		}
		if st == 5 && b == 0 {
			st = 0
			continue
		}
		if b == 1 {
			st++
		} else {
			st = 0
		}
		c |= b << cn
		cn++
		if cn == 8 {
			f = append(f, c)
			c, cn = 0, 0
		}
	}
	t.Logf("total closes %d", closes)
}

func TestDebugFrameHex(t *testing.T) {
	body, _ := buildTestFrame()
	lo, hi := FCSBytes(body)
	want := append(append([]byte(nil), body...), lo, hi)
	bits := probeBits(body, 40)
	// collect without validating
	var shift uint8
	var oR int
	var f []byte
	var c byte
	var cn, st int
	inF := false
	for _, b := range bits {
		shift = shift<<1 | b
		if b == 1 {
			oR++
		} else {
			oR = 0
		}
		if oR > 6 {
			inF = false
			f, c, cn, st = f[:0], 0, 0, 0
			continue
		}
		if shift == 0x7E || oR == 6 {
			if inF && len(f) >= 17 {
				break // frame closed
			}
			inF = true
			f, c, cn, st = f[:0], 0, 0, 0
			continue
		}
		if !inF {
			continue
		}
		if st == 5 && b == 0 {
			st = 0
			continue
		}
		if b == 1 {
			st++
		} else {
			st = 0
		}
		c |= b << cn
		cn++
		if cn == 8 {
			f = append(f, c)
			c, cn = 0, 0
		}
	}
	t.Logf("got : % x", f)
	t.Logf("want: % x", want)
	for i := 0; i < len(f) && i < len(want); i++ {
		if f[i] != want[i] {
			t.Fatalf("first byte diff at %d: got %02X want %02X", i, f[i], want[i])
		}
	}
}
