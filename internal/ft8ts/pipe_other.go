// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package ft8ts

// fcntlSetPipeSz is a no-op off Linux.
func fcntlSetPipeSz(fd uintptr, size int) (int, error) { return 0, nil }
