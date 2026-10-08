// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build linux

package ft8ts

import "syscall"

// f_SETPIPE_SZ (1031) sets a pipe's kernel buffer capacity. Done via
// the raw syscall because golang.org/x/sys is not a dependency here.
func fcntlSetPipeSz(fd uintptr, size int) (int, error) {
	r0, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, 1031, uintptr(size))
	if errno != 0 {
		return 0, errno
	}
	return int(r0), nil
}
