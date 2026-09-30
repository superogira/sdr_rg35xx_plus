package main

import (
	"os"
	"path/filepath"
	"time"

	"sdr35/internal/ui"
)

func main() {
	dir := filepath.Join(os.TempDir(), "flagview")
	os.MkdirAll(dir, 0755)
	entries := []ui.FT8Entry{
		{Time: "13:01:0" + "1", SNRDb: -8, FreqHz: 415, Text: "CQ HS0ZLG KO88", Anno: "Thailand 9500km", FlagCC: "TH"},
		{Time: "13:01:0" + "2", SNRDb: -12, FreqHz: 890, Text: "JA1ABC W1AW FN31", Anno: "Japan 8500km", FlagCC: "JP"},
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if ui.GetFlag("TH", dir) != nil && ui.GetFlag("JP", dir) != nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	u := ui.New(640, 480)
	img := u.Frame(ui.FrameStats{FreqHz: 14085000, Mode: "USB"})
	u.DrawFT8Log(entries, dir)
	u.DrawFT8LogFull(entries, 0, dir)
	ui.SavePNG(filepath.Join(dir, "ft8flags.png"), img)
}
