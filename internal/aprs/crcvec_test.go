// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import "testing"

// CRC-16/X.25 check value: the ASCII string "123456789" must yield
// 0x906E AFTER the final XOR (crc16 itself returns the pre-xorout
// register — the complement happens in FCSBytes at transmission).
// This is the independent proof that our FCS matches real AX.25, not
// just our own round trip: APRSdroid refused our beacons and the
// round trip could not have caught a shared CRC defect.
func TestCRC16X25CheckValue(t *testing.T) {
	got := crc16([]byte("123456789")) ^ 0xFFFF
	if got != 0x906E {
		t.Fatalf("crc16(\"123456789\")^FFFF = %04X, want 906E", got)
	}
}
