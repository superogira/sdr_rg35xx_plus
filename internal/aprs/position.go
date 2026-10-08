// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Position is what the radar needs out of an APRS report.
type Position struct {
	Lat, Lon  float64
	Table     byte // symbol table id as sent ('/' primary, '\\' alternate, or overlay)
	Sym       byte // symbol code
	CourseDeg float64
	SpeedKt   float64
	HasCS     bool
	AltFt     int
	HasAlt    bool
	Comment   string
}

// base91 decodes one compressed-field character.
func base91(c byte) float64 {
	v := float64(c - 33)
	if v < 0 {
		v = 0
	}
	if v > 90 {
		v = 90
	}
	return v
}

// ParsePosition extracts a position from an APRS information field.
// Supported: uncompressed (!, =, /, @ with timestamp) and compressed
// (base91) reports. Mic-E (destination-encoded) is not decoded yet.
func ParsePosition(info []byte) (*Position, bool) {
	if len(info) < 13 { // compressed minimum: type+table+10+sym
		return nil, false
	}
	body := info
	switch body[0] {
	case '!', '=':
		body = body[1:]
	case '/', '@': // with timestamp; position follows the 7-char stamp
		if len(body) < 8 {
			return nil, false
		}
		body = body[8:]
	case '`', '\'': // Mic-E position lives in the destination field
		return nil, false
	default:
		return nil, false
	}
	if len(body) < 12 {
		return nil, false
	}
	// Uncompressed fields look like "DDMM.hhN"; compressed ones start
	// with base91 printable chars. Body[7] is the hemisphere in an
	// uncompressed lat field.
	uncompressed := len(body) >= 19 &&
		(body[7] == 'N' || body[7] == 'S') &&
		body[2] >= '0' && body[2] <= '9' && body[4] == '.'
	p := &Position{}
	if uncompressed {
		p.Table = body[8]
		lat, ok := parseU(body[0:8])
		if !ok {
			return nil, false
		}
		lon, ok := parseU(body[9:18])
		if !ok {
			return nil, false
		}
		p.Lat, p.Lon = lat, lon
		p.Sym = body[18]
		rest := body[19:]
		// Optional course/speed "CCC/SSS" directly after the symbol.
		if len(rest) >= 7 && rest[3] == '/' {
			if c, err1 := strconv.Atoi(strings.TrimSpace(string(rest[0:3]))); err1 == nil {
				if s, err2 := strconv.Atoi(strings.TrimSpace(string(rest[4:7]))); err2 == nil {
					if c >= 0 && c <= 360 && s >= 0 && s <= 999 {
						p.CourseDeg, p.SpeedKt, p.HasCS = float64(c), float64(s), true
						rest = rest[7:]
					}
				}
			}
		}
		parseTail(rest, p)
		return p, true
	}
	// Compressed: table + lat5 + lon5 + sym (+ c s T)
	p.Table = body[0]
	var lv, xv float64
	for i := 1; i <= 5; i++ {
		lv = lv*91 + base91(body[i])
	}
	for i := 6; i <= 10; i++ {
		xv = xv*91 + base91(body[i])
	}
	p.Lat = 90 - lv/380926*90
	p.Lon = -180 + xv/380926*360 // 360° span (lat uses 90)
	p.Sym = body[11]
	parseTail(body[12:], p)
	return p, true
}

// parseU parses "DDMM.hhN" / "DDDMM.hhE" fields: the last 5 chars
// before the hemisphere are the minutes (MM.hh), the leading digits
// are degrees.
func parseU(f []byte) (float64, bool) {
	if len(f) != 8 && len(f) != 9 {
		return 0, false
	}
	hemi := f[len(f)-1]
	digits := string(f[:len(f)-1])
	if len(digits) < 6 || strings.ContainsAny(digits, " ") {
		return 0, false
	}
	minStr := digits[len(digits)-5:] // MM.hh
	degStr := digits[:len(digits)-5]
	deg, err1 := strconv.Atoi(degStr)
	mm, err2 := strconv.ParseFloat(minStr, 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	if mm >= 60 || deg >= map[bool]int{true: 180, false: 90}[len(f) == 9] {
		return 0, false
	}
	v := float64(deg) + mm/60
	switch hemi {
	case 'S', 'W':
		v = -v
	case 'N', 'E':
	default:
		return 0, false
	}
	return v, true
}

// parseTail picks altitude (/A=nnnnnn) out of the comment tail and
// stores the rest as the display comment.
func parseTail(rest []byte, p *Position) {
	s := string(rest)
	if i := strings.Index(s, "/A="); i >= 0 && len(s)-i >= 3+6 {
		if ft, err := strconv.Atoi(s[i+3 : i+9]); err == nil {
			p.AltFt, p.HasAlt = ft, true
			s = s[:i] + s[i+9:] // keep the altitude out of the comment
		}
	}
	p.Comment = strings.TrimSpace(s)
}

// fmtCoord renders degrees as the DDMM.hh / DDDMM.hh APRS field
// (degrees, then MM.hh minutes, then hemisphere).
func fmtCoord(v float64, isLon bool) string {
	hemi := byte('N')
	if isLon {
		hemi = 'E'
	}
	if v < 0 {
		v = -v
		hemi = 'S'
		if isLon {
			hemi = 'W'
		}
	}
	deg := int(v)
	min := (v - float64(deg)) * 60
	if isLon {
		return fmt.Sprintf("%03d%05.2f%c", deg, min, hemi)
	}
	return fmt.Sprintf("%02d%05.2f%c", deg, min, hemi)
}

// FormatPosition renders an uncompressed position report body WITHOUT
// the leading data-type identifier: "ddmm.hhN/dddmm.hhE> 045/030cmt".
// Course/speed only when moving, altitude appended as /A=.
func FormatPosition(p Position, comment string) string {
	lat := fmtCoord(p.Lat, false)
	lon := fmtCoord(p.Lon, true)
	table := p.Table
	if table == 0 {
		table = '/'
	}
	sym := p.Sym
	if sym == 0 {
		sym = '['
	}
	var sb strings.Builder
	sb.WriteString(lat)
	sb.WriteByte(table)
	sb.WriteString(lon)
	sb.WriteByte(sym)
	if p.HasCS && p.SpeedKt >= 1 {
		sb.WriteString(fmt.Sprintf("%03d/%03d", int(math.Round(p.CourseDeg))%360, int(math.Round(p.SpeedKt))))
	}
	c := strings.TrimSpace(comment)
	if c != "" {
		sb.WriteByte(' ')
		sb.WriteString(c)
	}
	if p.HasAlt {
		sb.WriteString(fmt.Sprintf(" /A=%06d", p.AltFt))
	}
	return sb.String()
}
