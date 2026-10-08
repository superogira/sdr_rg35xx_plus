// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package main

// lockInstance is a no-op on dev builds.
func lockInstance() bool { return true }
