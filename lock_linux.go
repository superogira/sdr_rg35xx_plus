//go:build linux

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockInstance takes an exclusive non-blocking flock in /tmp. Two
// instances fighting over /dev/fb0 render alternate frames — a hard
// flicker that also doubles the CPU load (an SSH launch plus a menu
// launch is enough to trigger it). /tmp, not the app dir: the SD card
// is exFAT, which silently grants every flock.
// lockHold keeps the lock fd alive for the process lifetime — closing
// it (or letting the *os.File be garbage-collected) releases the flock.
var lockHold *os.File

func lockInstance() bool {
	f, err := os.OpenFile("/tmp/sdrg35xx.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return true // can't lock? don't block the app over it
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		fmt.Fprintf(os.Stderr, "another SDRg35xx instance is already running — exiting\n")
		return false
	}
	lockHold = f
	return true
}
