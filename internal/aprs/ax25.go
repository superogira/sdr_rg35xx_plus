// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package aprs decodes and encodes APRS over Bell-202 AFSK (1200 baud,
// 1200/2200 Hz): AX.25 UI frames, position reports, a station store for
// the radar, and a phase-continuous AFSK modulator that turns a GPS fix
// into beacon audio for a VOX-keyed radio on the headphone jack.
package aprs

import (
	"fmt"
	"strings"
)

// Frame is a decoded AX.25 packet (addresses de-shifted, FCS removed).
type Frame struct {
	Dest    string // callsign-SSID
	Src     string // callsign-SSID
	Digis   []string
	Control byte
	PID     byte
	Info    []byte
}

// crc16 is CRC-16/X.25 (reflected 0x1021 = 0x8408, init 0xFFFF, bits
// fed LSB-first). A frame including its FCS bytes yields 0xF0B8.
func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// fcsMagic is the register residue when the CRC runs over a frame that
// already carries a correct FCS.
const fcsMagic = 0xF0B8

// DecodeFrame parses a validated frame body (dest..info, no FCS) into
// address fields. Returns nil if the addresses are malformed.
func DecodeFrame(body []byte) *Frame {
	// Need dest+src (14 bytes) + control + pid minimum for UI frames.
	if len(body) < 16 {
		return nil
	}
	// 13 SSID bits mark address-field extension: count 7-byte fields
	// until the one with the HDLC extension bit set.
	nAddr := 0
	for i := 0; i < len(body); i += 7 {
		nAddr++
		if i+6 >= len(body) || body[i+6]&0x01 != 0 {
			break
		}
		if nAddr >= 8 {
			return nil
		}
	}
	if len(body) < nAddr*7+2 {
		return nil
	}
	field := func(i int) string {
		b := body[i*7 : i*7+7]
		var sb strings.Builder
		for _, c := range b[:6] {
			ch := c >> 1
			if ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
				sb.WriteByte(ch)
			}
		}
		ssid := (b[6] >> 1) & 0x0F
		if ssid == 0 {
			return sb.String()
		}
		return fmt.Sprintf("%s-%d", sb.String(), ssid)
	}
	f := &Frame{
		Dest:    field(0),
		Src:     field(1),
		Control: body[nAddr*7],
		PID:     body[nAddr*7+1],
	}
	for i := 2; i < nAddr; i++ {
		f.Digis = append(f.Digis, field(i))
	}
	f.Info = append([]byte(nil), body[nAddr*7+2:]...)
	return f
}

// encAddr packs one callsign-SSID into the 7-byte AX.25 field.
func encAddr(call string, last bool) []byte {
	ssid := 0
	if i := strings.IndexByte(call, '-'); i >= 0 {
		fmt.Sscanf(call[i+1:], "%d", &ssid)
		call = call[:i]
	}
	call = strings.ToUpper(strings.TrimSpace(call))
	b := make([]byte, 7)
	for i := 0; i < 6; i++ {
		c := byte(' ')
		if i < len(call) {
			c = call[i]
		}
		b[i] = c << 1
	}
	b[6] = 0x60 | byte(ssid&0x0F)<<1
	if last {
		b[6] |= 0x01
	}
	return b
}

// EncodeUI builds a UI frame body (dest..info, no FCS, no stuffing —
// the modulator adds flags, FCS and stuffing).
func EncodeUI(src, dest string, digis []string, info []byte) []byte {
	var out []byte
	out = append(out, encAddr(dest, len(digis) == 0)...)
	out = append(out, encAddr(src, len(digis) == 0)...)
	for i, d := range digis {
		out = append(out, encAddr(d, i == len(digis)-1)...)
	}
	out = append(out, 0x03, 0xF0)
	out = append(out, info...)
	return out
}

// FCSBytes returns the two FCS bytes for a frame body as they must be
// appended on air (little-endian, ones-complemented).
func FCSBytes(body []byte) (lo, hi byte) {
	fcs := crc16(body)
	lo = byte(^(fcs & 0xFF))
	hi = byte(^((fcs >> 8) & 0xFF))
	return
}

// ValidFCS checks a body+FCS byte sequence with the magic-residue rule.
func ValidFCS(all []byte) bool {
	if len(all) < 2 {
		return false
	}
	// Feed the received FCS through the same LSB-first CRC; a good
	// frame leaves the 0xF0B8 residue.
	return crc16(all) == fcsMagic
}
