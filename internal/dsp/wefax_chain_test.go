// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package dsp

import (
	"image/png"
	"math"
	"os"
	"testing"
)

// TestWefaxThroughUSBChain sends the demo chart as RF through the real
// USB monitor branch and checks the saved image against what was sent.
func TestWefaxThroughUSBChain(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: ~150 s of signal")
	}
	SetIQRate(2048000)
	fax := demoWefaxAudio()
	ch := NewChain(ModeUSB, nil, nil)
	dec := NewWefaxDecoder()
	dec.SetAutoSave(true)
	ch.SetWefaxDecoder(dec)
	const block = 51200
	iq := make([]byte, 2*block)
	pos, ph := 0.0, 0.0
	var audio []float32
	for sent := 0; sent < len(fax)*IQRate/WefaxRate; sent += block {
		for i := 0; i < block; i++ {
			f := fax[int(pos)%len(fax)]
			pos += float64(WefaxRate) / float64(IQRate)
			ph += 2 * math.Pi * f / float64(IQRate)
			iq[2*i] = byte(clampU8(0.30*math.Cos(ph)*119 + 127.5))
			iq[2*i+1] = byte(clampU8(0.30*math.Sin(ph)*119 + 127.5))
		}
		audio = audio[:0]
		ch.Process(iq, &audio)
	}
	imgs := dec.TakeDone()
	if len(imgs) != 1 {
		t.Fatalf("completed images = %d, want 1", len(imgs))
	}
	im := imgs[0]
	W, H := im.Bounds().Dx(), im.Bounds().Dy()
	if os.Getenv("SDR_WEFAXSHOT") != "" {
		f, _ := os.Create("wefax_chain.png")
		png.Encode(f, im)
		f.Close()
	}
	if H < 232 || H > 244 {
		t.Fatalf("lines = %d, want ≈240", H)
	}
	// best vertical alignment (a line or two of start slack is fine)
	bestAcc := 0.0
	for off := -3; off <= 3; off++ {
		good, total := 0, 0
		for y := 8; y < H-8; y++ {
			l := y + off
			for x := int(float64(W) * 0.03); x < int(float64(W)*0.97); x += 3 {
				xf := float64(x) / float64(W)
				u := math.Mod(xf*8+float64(l)/30, 1)
				if u < 0.06 || u > 0.94 {
					continue
				}
				want := 1.0
				if int(xf*8+float64(l)/30)%2 == 0 {
					want = 0.15
				}
				total++
				if math.Abs(float64(im.GrayAt(x, y).Y)/255-want) < 0.3 {
					good++
				}
			}
		}
		if acc := float64(good) / float64(total); acc > bestAcc {
			bestAcc = acc
		}
	}
	if bestAcc < 0.95 {
		t.Fatalf("pattern accuracy %.3f, want ≥0.95", bestAcc)
	}
	t.Logf("lines=%d accuracy=%.4f", H, bestAcc)
}
