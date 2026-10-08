// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"math"
	"strings"
	"testing"
)

// rttyEncodeTones turns text into 8 kHz FSK audio: start bit (space),
// 5 data bits LSB first, 1.5 stop bits (mark), with automatic
// LTRS/FIGS shifts and USOS-consistent encoding. markHz/spaceHz allow
// generating swapped-tone signals for the reversal test.
func rttyEncodeTones(text string, markHz, spaceHz float64) []float64 {
	const bitLen = 176 // 8000 / 45.4545

	type tok struct {
		bits uint8
	}
	var seq []tok
	figs := false
	for _, r := range text {
		isFig := (r >= '0' && r <= '9') || strings.ContainsRune("-'!():\"#?&./;", r)
		switch {
		case isFig && !figs:
			seq = append(seq, tok{0x1b}) // FIGS
			figs = true
		case !isFig && figs && r != ' ':
			seq = append(seq, tok{0x1f}) // LTRS
			figs = false
		case r == ' ':
			figs = false // USOS: space implies letters
		}
		table := rttyLetters
		if figs {
			table = rttyFigures
		}
		found := false
		for i, s := range table {
			if s == string(r) {
				seq = append(seq, tok{uint8(i)})
				found = true
				break
			}
		}
		if !found {
			panic("no Baudot code for " + string(r))
		}
	}

	var samples []float64
	tone := func(hz float64, n int) {
		for i := 0; i < n; i++ {
			ph := 2 * math.Pi * hz * float64(len(samples)) / rttyFeedRate
			samples = append(samples, 0.5*math.Sin(ph))
		}
	}
	bit := func(mark bool) {
		if mark {
			tone(markHz, bitLen)
		} else {
			tone(spaceHz, bitLen)
		}
	}
	tone(markHz, rttyFeedRate) // ≥1 bit of idle mark
	for _, tk := range seq {
		bit(false) // start
		for k := 0; k < 5; k++ {
			bit(tk.bits&(1<<k) != 0)
		}
		bit(true)              // stop: 1 full bit
		tone(markHz, bitLen/2) // …plus half
	}
	tone(markHz, rttyFeedRate) // trailing idle mark (real signals never end mid-stop)
	return samples
}

func rttyEncode(text string) []float64 {
	return rttyEncodeTones(text, rttyMarkHz, rttySpaceHz)
}

func feedRTTY(d *RTTYDecoder, audio []float64) {
	for i := 0; i < len(audio); i += 512 {
		end := i + 512
		if end > len(audio) {
			end = len(audio)
		}
		d.Feed(audio[i:end])
	}
}

func TestRTTYDecodesLetters(t *testing.T) {
	d := NewRTTYDecoder()
	d.SetEnabled(true)
	feedRTTY(d, rttyEncode("CQ CQ DE HS0ZKO K\r"))
	lines := d.TakeLines()
	if len(lines) == 0 || lines[0] != "CQ CQ DE HS0ZKO K" {
		t.Fatalf("letters decode wrong: %q", lines)
	}
}

func TestRTTYDecodesFiguresAndUSOS(t *testing.T) {
	d := NewRTTYDecoder()
	d.SetEnabled(true)
	feedRTTY(d, rttyEncode("599 OK\r"))
	lines := d.TakeLines()
	if len(lines) == 0 || lines[0] != "599 OK" {
		t.Fatalf("figures/USOS decode wrong: %q", lines)
	}
}

func TestRTTYReversed(t *testing.T) {
	// Tones swapped over the air (LSB or reversed keyed transmitter):
	// decoding needs the REV flag.
	d := NewRTTYDecoder()
	d.SetEnabled(true)
	d.SetReversed(true)
	feedRTTY(d, rttyEncodeTones("REV TEST\r", rttySpaceHz, rttyMarkHz))
	lines := d.TakeLines()
	if len(lines) == 0 || lines[0] != "REV TEST" {
		t.Fatalf("rev decode wrong: %q", lines)
	}
}

func TestRTTYNoisySignal(t *testing.T) {
	d := NewRTTYDecoder()
	d.SetEnabled(true)
	audio := rttyEncode("NOISE TEST 123\r")
	rnd := uint64(12345)
	for i := range audio {
		rnd = rnd*6364136223846793005 + 1442695040888963407
		audio[i] += 0.4 * (float64(int64(rnd>>33)%2000)/1000.0 - 1)
	}
	feedRTTY(d, audio)
	lines := d.TakeLines()
	if len(lines) == 0 || lines[0] != "NOISE TEST 123" {
		t.Fatalf("noisy decode wrong: %q", lines)
	}
}

func TestRTTYSilentOnNoise(t *testing.T) {
	// Pure noise must not decode printable text: the 3-look vote and
	// the both-tones gate keep the screen clean (the first live test
	// printed endless structured garbage from noise before them).
	d := NewRTTYDecoder()
	d.SetEnabled(true)
	rnd := uint64(99)
	audio := make([]float64, 8000*8)
	for i := range audio {
		rnd = rnd*6364136223846793005 + 1442695040888963407
		audio[i] = 0.5 * (float64(int64(rnd>>33)%2000)/1000.0 - 1)
	}
	feedRTTY(d, audio)
	lines := d.TakeLines()
	total := 0
	for _, ln := range lines {
		total += len(ln)
	}
	if total > 4 {
		t.Fatalf("noise decoded %d chars (%q) — gating too weak", total, lines)
	}
}
