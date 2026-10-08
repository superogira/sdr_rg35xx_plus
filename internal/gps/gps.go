// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package gps parses NMEA 0183 sentences from a USB GPS receiver and
// keeps the latest fix. Only the sentences every receiver emits are
// used: GGA (position/fix/sats/HDOP/altitude), RMC (valid/speed/course)
// and GSA (sats in use). GSV feeds the satellites-in-view count.
package gps

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fix is the latest receiver state. Lat/Lon are decimal degrees
// (north/east positive), Alt metres, SpeedKt knots, CourseDeg true.
type Fix struct {
	Lat, Lon, Alt float64
	SpeedKt       float64
	CourseDeg     float64
	SatsUsed      int // GGA satellites used in the fix
	SatsView      int // GSV satellites in view
	Quality       int // 0 none, 1 GPS fix, 2 DGPS
	HDOP          float64
	TimeUTC       string // hhmmss.ss from GGA/RMC
	DateUTC       string // ddmmyy from RMC
	Valid         bool   // RMC status A
	Updated       time.Time
}

// Safe for concurrent use: the serial goroutine feeds lines, the UI and
// the radar read the snapshot.
type Receiver struct {
	mu   sync.Mutex
	fix  Fix
	open string // device path currently being read ("" = none)
}

func New() *Receiver { return &Receiver{} }

// Snapshot returns the current fix.
func (r *Receiver) Snapshot() Fix {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fix
}

// Device returns the serial device currently in use ("" when none).
func (r *Receiver) Device() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.open
}

func (r *Receiver) setDevice(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open = p
}

func (r *Receiver) setOpen(valid bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fix.Valid = valid
	if valid {
		r.fix.Updated = time.Now()
	}
}

// ChecksumOK validates "$...*hh".
func ChecksumOK(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 4 || s[0] != '$' {
		return false
	}
	star := strings.LastIndexByte(s, '*')
	if star < 0 {
		return false // many receivers emit a few unchecked lines; require it
	}
	var sum byte
	for i := 1; i < star; i++ {
		sum ^= s[i]
	}
	want, err := strconv.ParseUint(s[star+1:], 16, 8)
	return err == nil && sum == byte(want)
}

// Feed parses one NMEA line and merges it into the fix.
func (r *Receiver) Feed(line string) {
	line = strings.TrimSpace(line)
	if !ChecksumOK(line) {
		return
	}
	star := strings.LastIndexByte(line, '*')
	f := strings.Split(line[1:star], ",")
	if len(f) < 2 {
		return
	}
	talker := f[0]
	if len(talker) < 5 {
		return
	}
	switch talker[2:] {
	case "GGA":
		r.feedGGA(f)
	case "RMC":
		r.feedRMC(f)
	case "GSA":
		r.feedGSA(f)
	case "GSV":
		r.feedGSV(f)
	}
}

// dm parses NMEA ddmm.mmmm (or dddmm.mmmm) with hemisphere suffix.
func dm(val, hemi string) (float64, bool) {
	if val == "" {
		return 0, false
	}
	dot := strings.IndexByte(val, '.')
	if dot < 3 {
		return 0, false
	}
	deg, err := strconv.ParseFloat(val[:dot-2], 64)
	if err != nil {
		return 0, false
	}
	min, err := strconv.ParseFloat(val[dot-2:], 64)
	if err != nil {
		return 0, false
	}
	d := deg + min/60
	if hemi == "S" || hemi == "W" {
		d = -d
	}
	return d, true
}

func num(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func (r *Receiver) feedGGA(f []string) {
	if len(f) < 15 {
		return
	}
	lat, ok1 := dm(f[2], f[3])
	lon, ok2 := dm(f[4], f[5])
	q := int(num(f[6]))
	if !ok1 || !ok2 || q == 0 {
		// Lost fix: keep coordinates but mark invalid so the radar
		// stops following a stale position.
		r.setOpen(false)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fix.Lat, r.fix.Lon = lat, lon
	r.fix.Quality = q
	r.fix.SatsUsed = int(num(f[7]))
	r.fix.HDOP = num(f[8])
	r.fix.Alt = num(f[9])
	r.fix.TimeUTC = f[1]
	r.fix.Updated = time.Now()
	r.fix.Valid = true
}

func (r *Receiver) feedRMC(f []string) {
	if len(f) < 10 {
		return
	}
	if f[2] != "A" {
		r.setOpen(false)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fix.SpeedKt = num(f[7])
	r.fix.CourseDeg = num(f[8])
	if len(f) > 9 && len(f[9]) >= 6 {
		r.fix.DateUTC = f[9] // ddmmyy
	}
	if f[1] != "" {
		r.fix.TimeUTC = f[1]
	}
	r.fix.Updated = time.Now()
	r.fix.Valid = true
}

func (r *Receiver) feedGSA(f []string) {
	if len(f) < 18 {
		return
	}
	n := 0
	for _, id := range f[3:15] { // 12 satellite-id slots
		if id != "" {
			n++
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fix.SatsUsed = n
	r.fix.HDOP = num(f[16])
}

func (r *Receiver) feedGSV(f []string) {
	if len(f) < 4 {
		return
	}
	// Only the first packet of a GSV burst carries the total.
	if f[2] == "1" {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.fix.SatsView = int(num(f[3]))
	}
}

// Grid returns the Maidenhead locator for the current fix (6 chars,
// e.g. "OK03ev"). Empty when there is no position yet.
func (r *Receiver) Grid() string {
	f := r.Snapshot()
	return Maidenhead(f.Lat, f.Lon)
}

// TimeLabel formats the GPS UTC time/date for display, e.g.
// "13:47:32 · 04/10/26 UTC". Empty when nothing received yet.
func TimeLabel(f Fix) string {
	var parts []string
	if len(f.TimeUTC) >= 6 {
		parts = append(parts, f.TimeUTC[0:2]+":"+f.TimeUTC[2:4]+":"+f.TimeUTC[4:6])
	}
	if len(f.DateUTC) >= 6 {
		parts = append(parts, f.DateUTC[0:2]+"/"+f.DateUTC[2:4]+"/"+f.DateUTC[4:6])
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + " UTC"
}

// Maidenhead converts decimal degrees to a 6-character grid locator.
func Maidenhead(lat, lon float64) string {
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return ""
	}
	lon += 180
	lat += 90
	const A = 'A'
	fld := int(lon / 20)
	lon -= float64(fld) * 20
	sq := int(lon / 2)
	lon -= float64(sq) * 2
	sub := int(lon / (2.0 / 24))
	gr := int(lat / 10)
	lat -= float64(gr) * 10
	ss := int(lat) // 1° grid square row
	lat -= float64(ss)
	sub2 := int(lat / (1.0 / 24)) // 2.5' subsquare
	return fmt.Sprintf("%c%c%d%d%c%c",
		A+fld, A+gr, sq, ss, 'a'+sub, 'a'+sub2)
}
