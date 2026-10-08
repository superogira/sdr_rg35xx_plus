// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package ui

import "fmt"

func openFB() (Display, error) {
	return nil, fmt.Errorf("framebuffer display requires Linux (use -display png:file.png)")
}
