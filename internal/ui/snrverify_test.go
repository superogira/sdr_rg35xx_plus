package ui

import (
	"image/png"
	"os"
	"testing"
	"time"
)

func TestSNRVisible(t *testing.T) {
	u := New(640, 480)
	entries := []FT8Entry{
		{Time: "21:15:03", SNRDb: 9, Text: "F4CQS BG7ZHS R -20"},
		{Time: "21:15:18", SNRDb: 4, Text: "CQ DX VU2KPH NL25"},
	}
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	u.DrawFT8Log(entries)
	// Blue-ish pixels (120,200,255) must exist in the overlay region
	// (bottom-left, y > H-120): the SNR column.
	found := false
	for y := 480 - 120; y < 480-40 && !found; y++ {
		for x := 0; x < 340 && !found; x++ {
			r, g, b, _ := frame.At(x, y).RGBA()
			if r>>8 > 60 && r>>8 < 180 && g>>8 > 150 && b>>8 > 220 {
				found = true
			}
		}
	}
	if !found {
		t.Error("SNR column pixels not found in the mini overlay")
	}
	if os.Getenv("SDR_SNRSHOT") != "" {
		f, _ := os.Create("snrmini.png")
		defer f.Close()
		png.Encode(f, frame)

		u2 := New(640, 480)
		full := []FT8Entry{}
		for i := 0; i < 20; i++ {
			full = append(full, FT8Entry{Time: time.Now().Format("15:04:05"), SNRDb: float64(12 - i/2), Text: "E20ZKT BA7SAY OL53"})
		}
		fr2 := u2.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
		u2.DrawFT8LogFull(full, 0)
		f2, _ := os.Create("snrfull.png")
		defer f2.Close()
		png.Encode(f2, fr2)
	}
}
