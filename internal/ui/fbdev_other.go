//go:build !linux

package ui

import "fmt"

func openFB() (Display, error) {
	return nil, fmt.Errorf("framebuffer display requires Linux (use -display png:file.png)")
}
