// Package backlight controls the RG35XX Plus panel brightness through
// the Allwinner dispdbg interface (the only write path this firmware
// honours) plus the fb blank level for full-off. Ported from goro's
// platform_fbdev.go power-cycle feature.
package backlight

import (
	"os"
	"strconv"
	"strings"
)

const dispdbg = "/sys/kernel/debug/dispdbg"

// captureLevel reads the current brightness via getbl; the firmware
// sometimes returns nothing usable, so the caller supplies a fallback.
func captureLevel() int {
	for _, w := range [][2]string{
		{dispdbg + "/name", "lcd0"},
		{dispdbg + "/command", "getbl"},
		{dispdbg + "/start", "1"},
	} {
		if err := os.WriteFile(w[0], []byte(w[1]), 0o644); err != nil {
			return 0
		}
	}
	data, err := os.ReadFile(dispdbg + "/param")
	if err != nil {
		return 0
	}
	level, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || level <= 0 || level > 255 {
		return 0
	}
	return level
}

// Set writes the panel brightness (0-255) via dispdbg setbl.
func Set(level int) {
	if _, err := os.Stat(dispdbg + "/command"); err != nil {
		return
	}
	for _, w := range [][2]string{
		{dispdbg + "/name", "lcd0"},
		{dispdbg + "/param", strconv.Itoa(level)},
		{dispdbg + "/command", "setbl"},
		{dispdbg + "/start", "1"},
	} {
		if err := os.WriteFile(w[0], []byte(w[1]), 0o644); err != nil {
			return
		}
	}
}

// BlankFB blanks (true) or unblanks (false) the fb0 layer for full-off.
func BlankFB(blank bool) {
	v := byte('0')
	if blank {
		v = '4'
	}
	os.WriteFile("/sys/class/graphics/fb0/blank", []byte{v}, 0o644)
}

// Cycle steps the power-key panel state: on → dim (backlight 10,
// image keeps rendering) → off (backlight 0 + fb blanked) → on
// (restore the captured level). state is the CURRENT state index
// (0=on, 1=dim, 2=off); returns the next state. Audio is NOT muted —
// the app keeps receiving (the whole point for battery-saving RX).
func Cycle(state int) int {
	next := (state + 1) % 3
	switch next {
	case 1:
		if state == 0 {
			// Never adopt a dim-level brightness as the "normal" one:
			// quitting the app in the dim state leaves the panel at 10,
			// and a re-launch would otherwise capture that stale level
			// and restore to it forever (screen stuck dim).
			if v := captureLevel(); v > 10 {
				restoreLevel = v
			}
		}
		Set(10)
	case 2:
		Set(0)
		BlankFB(true)
	case 0:
		RestoreOn()
	}
	return next
}

// RestoreOn un-blanks the panel and heals a stuck-dim brightness (a
// level <= 10 can only come from this app's dim state — the firmware
// default is far higher). Called at app start and on quit.
func RestoreOn() {
	BlankFB(false)
	if v := captureLevel(); v <= 10 {
		Set(200)
	}
}

var restoreLevel int
