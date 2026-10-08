// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !linux

package gps

import "context"

// Run is a no-op on non-Linux dev builds — there is no serial GPS to
// read on Windows; the parser is still exercised by unit tests.
func (r *Receiver) Run(ctx context.Context) {
	<-ctx.Done()
}
