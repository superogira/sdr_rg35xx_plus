// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package adsb

import (
	"fmt"
	"testing"
)

func BenchmarkFeedIQ(b *testing.B) {
	rate := 2_400_000.0
	idle := ppmIdle(int(0.05*rate), 0.12, 7) // 50 ms of noisy IQ
	d := NewModeSDemod(rate, NewStore())
	b.SetBytes(int64(len(idle)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.FeedIQ(idle)
	}
	b.StopTimer()
	fmt.Printf("  pre=%d crc=%d dec=%d\n", d.Preambles, d.CrcFails, d.Decoded)
}
