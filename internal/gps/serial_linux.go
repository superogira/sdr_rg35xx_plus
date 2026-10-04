//go:build linux

package gps

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// candidate devices in priority order; the GPS dongle shows up as a
// USB-CDC modem (ttyACM, u-blox style) or a classic UART bridge
// (ttyUSB, CP210x/CH340/FTDI).
var portCandidates = []string{
	"/dev/ttyACM0", "/dev/ttyACM1",
	"/dev/ttyUSB0", "/dev/ttyUSB1",
}

// bauds to probe, most common first. 9600 is the NMEA default; u-blox
// defaults to 38400 and some receivers are set to 4800 or 115200.
var baudCandidates = []int{9600, 38400, 4800, 115200, 57600, 19200}

func baudConst(b int) uint32 {
	switch b {
	case 4800:
		return unix.B4800
	case 9600:
		return unix.B9600
	case 19200:
		return unix.B19200
	case 38400:
		return unix.B38400
	case 57600:
		return unix.B57600
	case 115200:
		return unix.B115200
	}
	return unix.B9600
}

func openPort(path string, baud int) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	// Leave O_NONBLOCK off afterwards: read should block in the scan
	// goroutine (it also makes Close unblock a waiting read).
	unix.SetNonblock(fd, false)

	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	// Raw 8N1; on Linux the speed IS part of c_cflag (B9600…).
	t.Cflag = baudConst(baud) | unix.CS8 | unix.CREAD | unix.CLOCAL
	t.Iflag = unix.IGNPAR
	t.Oflag = 0
	t.Lflag = 0 // raw
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 5 // 0.5 s read timeout
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// portHasGPS opens a candidate at a candidate baud and reads briefly,
// looking for NMEA sentences. Returns the open port on success.
func portHasGPS(path string) (*os.File, error) {
	var lastErr error
	for _, b := range baudCandidates {
		f, err := openPort(path, b)
		if err != nil {
			return nil, err // device gone / no permission
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 512), 512)
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			f.SetReadDeadline(deadline)
			if sc.Scan() {
				line := sc.Text()
				if len(line) > 6 && line[0] == '$' && ChecksumOK(line) {
					talk := line[3:min(6, len(line))]
					if talk == "GGA" || talk == "RMC" || talk == "GSV" || talk == "GSA" {
						return f, nil // keep open at this baud
					}
				}
			} else {
				break
			}
		}
		f.Close()
		if lastErr == nil {
			lastErr = fmt.Errorf("no NMEA at %d baud", b)
		}
	}
	return nil, lastErr
}

// Run scans for a GPS serial device, reads NMEA into the receiver and
// rescans when the device disappears (the hub being replugged etc).
// Stop with ctx.
func (r *Receiver) Run(ctx context.Context) {
	for ctx.Err() == nil {
		var f *os.File
		var found string
		for _, p := range portCandidates {
			if _, err := os.Stat(p); err != nil {
				continue
			}
			g, err := portHasGPS(p)
			if err == nil {
				f, found = g, p
				break
			}
		}
		if f == nil {
			r.setDevice("")
			sleepCtx(ctx, 5*time.Second)
			continue
		}
		r.setDevice(found)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 512), 512)
		for ctx.Err() == nil && sc.Scan() {
			r.Feed(sc.Text())
		}
		// Read error: device vanished (or cable pulled). Close, rescan.
		f.Close()
		r.setDevice("")
		r.setOpen(false)
		sleepCtx(ctx, 2*time.Second)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
