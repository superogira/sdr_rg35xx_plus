package main

import (
	"fmt"
	"os"
	"path/filepath"

	"sdr35/internal/ui"
)

func main() {
	dir := filepath.Join(os.TempDir(), "flagprefetch")
	os.MkdirAll(dir, 0755)
	ui.PrefetchAllFlags(dir)
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".png" {
			n++
		}
	}
	fmt.Println("png files:", n)
	if _, err := os.Stat(filepath.Join(dir, "all.done")); err != nil {
		fmt.Println("MARKER MISSING")
		os.Exit(1)
	}
	fmt.Println("marker ok, dir:", dir)
}
