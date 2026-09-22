// Package ui renders the SDR screen: a scrolling waterfall on top and a
// status/frequency bar at the bottom, written straight to the framebuffer.
package ui

import (
	"fmt"
	"image"
	"image/png"
	"os"
)

// Display is one presentation target for the finished frame.
type Display interface {
	// Size is the framebuffer dimension in pixels.
	Size() (w, h int)
	// Present blits the frame (top-left origin, same size as Size).
	Present(frame *image.RGBA) error
	Close() error
}

// OpenDisplay resolves a display spec:
//
//	"auto"        — /dev/fb0 mmap on Linux (the device)
//	"png:file"    — write the presented frame to a PNG file (testing)
func OpenDisplay(spec string) (Display, error) {
	switch {
	case spec == "auto" || spec == "":
		return openFB()
	case len(spec) > 4 && spec[:4] == "png:":
		return &pngDisplay{path: spec[4:]}, nil
	}
	return nil, fmt.Errorf("unknown display spec %q", spec)
}

// pngDisplay writes each presented frame as a PNG. Screenshot testing only.
type pngDisplay struct {
	path string
}

func (p *pngDisplay) Size() (int, int) { return 640, 480 }

func (p *pngDisplay) Present(frame *image.RGBA) error {
	f, err := os.Create(p.path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, frame)
}

func (p *pngDisplay) Close() error { return nil }
