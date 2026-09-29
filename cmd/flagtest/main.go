package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"sdr35/internal/ui"
)

func main() {
	dir := filepath.Join(os.TempDir(), "flagtest2")
	os.MkdirAll(dir, 0755)
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		done := true
		for _, cc := range []string{"TH", "MX", "ZA", "JP", "US", "GB"} {
			if ui.GetFlag(cc, dir) == nil {
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	for _, cc := range []string{"TH", "MX", "ZA", "JP", "US", "GB"} {
		if f := ui.GetFlag(cc, dir); f != nil {
			p := filepath.Join(dir, cc+".png")
			os.Rename(filepath.Join(dir, cc+"_tmp.png"), p)
			out, _ := os.Create(filepath.Join(dir, cc+"_view.png"))
			png.Encode(out, f)
			out.Close()
			fmt.Println(cc, "ok", f.Bounds().Dx(), "x", f.Bounds().Dy())
		} else {
			fmt.Println(cc, "MISSING")
		}
	}
	fmt.Println("dir:", dir)
}
