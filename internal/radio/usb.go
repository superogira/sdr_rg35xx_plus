// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package radio

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// USBHost is the pseudo host value that selects the local RTL-SDR dongle:
// the app spawns its own rtl_tcp bound to 127.0.0.1:1234 and connects there,
// so no external server is needed.
const USBHost = "usb"

const usbDialAddr = "127.0.0.1:1234"

// USBSource reports whether the radio is fed by the local dongle.
func (r *Radio) USBSource() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usbSrc
}

// usbPortOpen reports whether something already listens on the local
// rtl_tcp port (our own earlier spawn, or a manually started server —
// both are fine to reuse).
func usbPortOpen() bool {
	c, err := net.DialTimeout("tcp", usbDialAddr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func waitUSBPort(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if usbPortOpen() {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// usbServerPaths returns candidate rtl_tcp executables, most preferred
// first: next to the app binary (bundled in the release), a dev build
// location on the test device, then whatever is on PATH.
func usbServerPaths() []string {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(dir, "rtl_tcp"),
			filepath.Join(dir, "rtl_tcp_static"),
		)
	}
	cands = append(cands, "/root/rtl-static/src/rtl_tcp_static", "rtl_tcp")
	return cands
}

// usbLogPath places the server log next to the binary when writable,
// otherwise in /tmp — seeing "usb_claim_interface error -6" there is the
// quickest way to diagnose a stale rtl_tcp holding the dongle.
func usbLogPath() string {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "rtl_tcp.log")
		if f, err := os.Create(p); err == nil {
			f.Close()
			return p
		}
	}
	return filepath.Join(os.TempDir(), "rtl_tcp.log")
}

// ensureUSBSrv makes sure a local rtl_tcp is listening before a connect
// attempt. Reuses an existing listener; otherwise spawns our own. If the
// spawn cannot take the dongle (a zombie rtl_tcp that still holds USB but
// serves nothing — it survives SIGTERM while blocked in a USB read), any
// stale rtl_tcp is SIGKILLed and one retry is made.
func (r *Radio) ensureUSBSrv() error {
	r.usbMu.Lock()
	defer r.usbMu.Unlock()
	if usbPortOpen() {
		return nil
	}
	r.killUSBProcLocked()
	for attempt := 0; attempt < 2; attempt++ {
		lastErr := fmt.Errorf("rtl_tcp executable not found")
		for _, p := range usbServerPaths() {
			if _, err := os.Stat(p); err != nil && p != "rtl_tcp" {
				continue
			}
			cmd, err := usbStartProcess(p, usbLogPath())
			if err != nil {
				lastErr = err
				continue
			}
			r.usbProc = cmd
			if waitUSBPort(4 * time.Second) {
				return nil
			}
			lastErr = fmt.Errorf("rtl_tcp did not start (see rtl_tcp.log)")
			break
		}
		// Free a possibly wedged rtl_tcp that owns the dongle without
		// listening — on this device rtl_tcp only ever belongs to us.
		if runtime.GOOS == "linux" {
			exec.Command("pkill", "-9", "-x", "rtl_tcp").Run()
			exec.Command("pkill", "-9", "-x", "rtl_tcp_static").Run()
		}
		r.killUSBProcLocked()
		if attempt == 1 {
			return lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// stopUSBSrv kills a locally spawned rtl_tcp (called when switching away
// from the USB source and when the app exits, so the dongle is released).
func (r *Radio) stopUSBSrv() {
	r.usbMu.Lock()
	defer r.usbMu.Unlock()
	r.killUSBProcLocked()
}

// StopUSB is the exported exit-path hook: main() defers it because the
// radio's Run goroutine is not waited for on quit and its own defer can
// race the process exit, orphaning the dongle holder.
func (r *Radio) StopUSB() { r.stopUSBSrv() }

// resetUSBSrv kills the local rtl_tcp — ours AND any orphan left by a
// kill -9'd app instance — so the next connect respawns it against
// freshly enumerated USB devices. A server whose device was unplugged
// mid-stream still accepts connections but streams nothing, and
// survives SIGTERM (stuck inside a USB read), so -9 is the only way.
func (r *Radio) resetUSBSrv() {
	r.stopUSBSrv()
	if runtime.GOOS == "linux" {
		exec.Command("pkill", "-9", "-x", "rtl_tcp").Run()
		exec.Command("pkill", "-9", "-x", "rtl_tcp_static").Run()
	}
}

// killUSBProcLocked reaps our child if it is still running. SIGKILL only:
// rtl_tcp ignores SIGTERM while blocked inside a USB transfer.
func (r *Radio) killUSBProcLocked() {
	if r.usbProc == nil {
		return
	}
	if p := r.usbProc.Process; p != nil {
		p.Kill()
	}
	// Reap to avoid a zombie between spawns.
	if r.usbProc.Process != nil {
		done := make(chan struct{})
		go func() { _, _ = r.usbProc.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}
	r.usbProc = nil
}
