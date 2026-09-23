package ui

import (
	"image/png"
	"os"
	"testing"
)

func TestSysMonRender(t *testing.T) {
	u := New(640, 480)
	rows := []string{
		"# System monitor ▸",
		"CPU temp\t52.3 °C",
		"GPU temp\t48.9 °C",
		"VE temp\t46.1 °C",
		"DDR temp\t44.7 °C",
		"Battery temp\t31.2 °C",
		"Battery level\t87 %",
		"Battery voltage\t4.12 V",
		"Battery status\tDischarging",
		"CPU usage\t38 %",
		"Memory\t52 %",
		"Swap\t—",
	}
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	u.DrawSysMon(rows)
	// assert bright pixels exist in the panel (title cyan + text)
	found := 0
	for y := 0; y < 460; y++ {
		for x := 0; x < 640; x++ {
			r, g, b, _ := frame.At(x, y).RGBA()
			if r>>8 > 60 || g>>8 > 150 || b>>8 > 150 {
				found++
			}
		}
	}
	if found < 500 {
		t.Errorf("panel looks empty: %d bright pixels", found)
	}
	if os.Getenv("SDR_MONSHOT") != "" {
		f, _ := os.Create("sysmon.png")
		defer f.Close()
		png.Encode(f, frame)
	}
}
