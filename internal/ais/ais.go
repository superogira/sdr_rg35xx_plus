// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package ais: NMEA AIVDM/AIVDO client and decoder for ship positions
// and names, feeding a TTL store like the ADS-B one.
package ais

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"strings"
	"sync"
	"time"
)

// Ship is one tracked vessel.
// TrailDot is one vessel position breadcrumb.
type TrailDot struct {
	Lat, Lon float64
	At       time.Time
}

type Ship struct {
	MMSI     string
	Name     string
	Lat, Lon float64
	HasPos   bool
	Trail    []TrailDot // position history, newest last (~5 min dots)
	SogKt    float64
	CogDeg   float64
	LastSeen time.Time
	AtoN     bool // static aid-to-navigation / base station, not a vessel

	// Static + voyage data (type 5 / 24B) and nav status (1/2/3) —
	// accumulated as the vessel's messages arrive.
	Callsign               string
	Imo                    uint32
	Destination            string
	EtaText                string
	ShipType               byte
	NavStat                byte
	DimA, DimB, DimC, DimD uint16  // bow/stern/port/starboard, metres
	Draught                float64 // metres
	Heading                int     // degrees, 511 = n/a
}

// Store holds ships by MMSI, pruned by TTL (ships are slow — 10 min).
type Store struct {
	mu    sync.Mutex
	ships map[string]*Ship
	frag1 map[string]string // AIVDM fragment-1 payloads by sequence id
	ttl   time.Duration
}

func NewStore() *Store {
	return &Store{ships: map[string]*Ship{}, frag1: map[string]string{}, ttl: 10 * time.Minute}
}

func (s *Store) Ships() []*Ship {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Ship
	for _, sh := range s.ships {
		if time.Since(sh.LastSeen) > s.ttl {
			continue
		}
		cp := *sh
		out = append(out, &cp)
	}
	return out
}

func (s *Store) ship(mmsi string) *Ship {
	sh := s.ships[mmsi]
	if sh == nil {
		sh = &Ship{MMSI: mmsi}
		s.ships[mmsi] = sh
	}
	return sh
}

// recordTrail keeps one breadcrumb per 5 minutes (vessels move slowly;
// 24 h ≈ 288 dots) — enough to draw a thin track on the web map.
func (sh *Ship) recordTrail(lat, lon float64) {
	now := time.Now()
	if n := len(sh.Trail); n > 0 && now.Sub(sh.Trail[n-1].At) < 5*time.Minute {
		return
	}
	sh.Trail = append(sh.Trail, TrailDot{Lat: lat, Lon: lon, At: now})
	if len(sh.Trail) > 288 {
		sh.Trail = sh.Trail[len(sh.Trail)-288:]
	}
}

// latLonAt reads the standard 28-bit signed latitude at latOff and
// longitude at lonOff (1/10000-minute units) with the "not available"
// guards. Types 6 and 21 carry the same pair at different offsets.
func latLonAt(p []byte, latOff, lonOff int) (float64, float64, bool) {
	if latOff+27 >= len(p)*6 || lonOff+27 >= len(p)*6 {
		return 0, 0, false
	}
	lat := float64(i(p, latOff, 28)) / 600000
	lon := float64(i(p, lonOff, 28)) / 600000
	if lat == 91 || lon == 181 || math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// u reads an unsigned field from the 6-bit payload values. Bits past
// the payload read as 0 — real-world AIS payloads are padded to
// variable lengths and the field layout runs past short ones.
func u(p []byte, off, n int) uint32 {
	var v uint32
	for i := 0; i < n; i++ {
		bit := off + i
		if bit/6 >= len(p) {
			return v
		}
		c := p[bit/6]
		b := (c >> (5 - uint(bit%6))) & 1
		v = v<<1 | uint32(b)
	}
	return v
}

// i reads a two's-complement field.
func i(p []byte, off, n int) int32 {
	v := u(p, off, n)
	if n > 0 && v&(1<<(n-1)) != 0 {
		return int32(v) - (1 << n)
	}
	return int32(v)
}

// text decodes a 6-bit ASCII field; '@' is padding.
func text(p []byte, off, n int) string {
	var sb strings.Builder
	for k := 0; k+6 <= n; k += 6 {
		v := u(p, off+k, 6)
		// ITU-R M.1371 six-bit text: values 0-31 map to '@'-'_'
		// (uppercase letters live at v 1-26) and 32-63 map to ' '-'?'
		// (digits at v 48-57). The earlier +48/+32 table produced
		// plausible-looking mojibake for every real ship name.
		var c byte
		if v < 32 {
			c = byte(v + 64)
		} else {
			c = byte(v)
		}
		sb.WriteByte(c)
	}
	// '@' doubles as padding and blank in practice.
	return strings.TrimSpace(strings.ReplaceAll(sb.String(), "@", " "))
}

// Decode consumes one NMEA sentence (with or without the !/$ prefix
// and checksum tail).
func (s *Store) Decode(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	body := strings.TrimLeft(line, "!$")
	if x := strings.IndexByte(body, '*'); x >= 0 {
		// Corrupted sentences would otherwise spawn bogus MMSIs.
		var sum byte
		for k := 0; k < x; k++ {
			sum ^= body[k]
		}
		var want byte
		if _, err := fmt.Sscanf(body[x+1:], "%02X", &want); err != nil || want != sum {
			return
		}
		body = body[:x]
	}
	f := strings.Split(body, ",")
	if len(f) < 6 {
		return
	}
	if !strings.HasPrefix(f[0], "AI") {
		return
	}
	// Multi-fragment messages (type 5 voyage data): hold fragment 1 by
	// sequence id, decode when the last fragment completes it.
	payload := f[5]
	if f[1] != "1" {
		num, id := f[2], f[3]
		if num == "1" {
			s.mu.Lock()
			s.frag1[id] = payload
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		first := s.frag1[id]
		delete(s.frag1, id)
		s.mu.Unlock()
		if first == "" {
			return
		}
		payload = first + payload
	}

	// AIVDM payload → 6-bit values.
	p := make([]byte, 0, len(payload))
	for k := 0; k < len(payload); k++ {
		c := payload[k]
		if c < 48 || c > 119 || (c > 87 && c < 96) {
			return
		}
		v := c - 48
		if v > 40 {
			v -= 8
		}
		p = append(p, v)
	}
	if len(p) < 20 {
		return
	}

	s.decodePayload(p)
}

// DecodeBits decodes a raw over-the-air AIS payload (the HDLC frame's
// content, one bit per byte is NOT expected — plain bytes here) and
// routes it through the same field decoder as AIVDM sentences. The
// bytes are repacked into the 6-bit cells u/i/text read.
func (s *Store) DecodeBits(payload []byte) (typ uint32, mmsi string) {
	n6 := len(payload) * 8 / 6
	if n6 < 20 {
		return 0, ""
	}
	// The demodulator assembles HDLC octets (LSB-first on air); the
	// message bit stream then reads each octet MSB-first into the
	// 6-bit armor cells — verified against AIS-catcher on a live
	// recording (Thai MMSIs and positions matched exactly).
	p := make([]byte, n6)
	for bit := 0; bit < n6*6; bit++ {
		b := (payload[bit/8] >> uint(7-bit%8)) & 1
		p[bit/6] = p[bit/6]<<1 | b
	}
	return s.decodePayload(p)
}

// decodePayload applies the AIS field layout to a 6-bit-cell payload
// and updates the ship table. Shared by the AIVDM and RF paths.
func (s *Store) decodePayload(p []byte) (typ uint32, mmsi string) {
	typ = u(p, 0, 6)
	mmsi = fmt.Sprintf("%09d", u(p, 8, 30))

	switch typ {
	case 1, 2, 3, 5, 6, 18, 21, 24:
	default:
		return typ, mmsi
	}
	if u(p, 8, 30) == 0 {
		return typ, mmsi
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.ship(mmsi)
	sh.LastSeen = time.Now()
	switch typ {
	case 1, 2, 3: // Class A position
		sh.NavStat = byte(u(p, 38, 4))
		if hd := u(p, 128, 9); hd != 511 {
			sh.Heading = int(hd)
		}
		sh.SogKt = float64(u(p, 50, 10)) / 10
		lon := float64(i(p, 61, 28)) / 600000
		lat := float64(i(p, 89, 27)) / 600000
		if lat != 91 && lon != 181 {
			sh.recordTrail(lat, lon)
			sh.Lat, sh.Lon, sh.HasPos = lat, lon, true
		}
		sh.CogDeg = float64(u(p, 116, 12)) / 10
	case 18: // Class B position
		if hd := u(p, 133, 9); hd != 511 {
			sh.Heading = int(hd)
		}
		sh.SogKt = float64(u(p, 46, 10)) / 10
		lon := float64(i(p, 57, 28)) / 600000
		lat := float64(i(p, 85, 27)) / 600000
		if lat != 91 && lon != 181 {
			sh.recordTrail(lat, lon)
			sh.Lat, sh.Lon, sh.HasPos = lat, lon, true
		}
		sh.CogDeg = float64(u(p, 112, 12)) / 10
	case 24: // Class B static — part A name, part B ship data
		if u(p, 38, 2) == 0 {
			sh.Name = text(p, 40, 120)
		} else {
			sh.ShipType = byte(u(p, 40, 8))
			sh.Callsign = text(p, 66, 42)
			sh.DimA = uint16(u(p, 132, 9))
			sh.DimB = uint16(u(p, 141, 9))
			sh.DimC = uint16(u(p, 150, 6))
			sh.DimD = uint16(u(p, 156, 6))
		}
	case 5: // Class A static + voyage (name + callsign)
		sh.Name = text(p, 112, 120)
		sh.Callsign = text(p, 70, 42)
		sh.Imo = u(p, 40, 30)
		sh.ShipType = byte(u(p, 232, 8))
		sh.DimA = uint16(u(p, 240, 9))
		sh.DimB = uint16(u(p, 249, 9))
		sh.DimC = uint16(u(p, 258, 6))
		sh.DimD = uint16(u(p, 264, 6))
		sh.Draught = float64(u(p, 274, 8)) / 10
		sh.Destination = text(p, 302, 120)
		sh.EtaText = etaText(u(p, 422, 4), u(p, 426, 5), u(p, 431, 5), u(p, 436, 6))
	case 6: // Assigned-mode BASE STATION — a station, not a vessel
		sh.AtoN = true
		sh.Lat, sh.Lon, sh.HasPos = latLonAt(p, 72, 100)
	case 21: // Class A aids-to-navigation (buoy, lighthouse, …)
		sh.AtoN = true
		sh.Name = text(p, 43, 120)
		sh.Lat, sh.Lon, sh.HasPos = latLonAt(p, 193, 165)
	}
	return typ, mmsi
}

// etaText formats the type-5 ETA fields (0 month / day, 24 hour, 60
// minute = not available).
func etaText(mon, day, hour, min uint32) string {
	if mon == 0 || day == 0 || mon > 12 || day > 31 || hour > 23 || min > 59 {
		return ""
	}
	return fmt.Sprintf("%02d-%02d %02d:%02d", mon, day, hour, min)
}

// Ship returns a copy of one ship's current state (nil if unknown).
func (s *Store) Ship(mmsi string) *Ship {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh := s.ships[mmsi]
	if sh == nil {
		return nil
	}
	cp := *sh
	return &cp
}

// Client connects to an NMEA-over-TCP server and feeds the store.
type Client struct {
	mu   sync.Mutex
	host string

	Connected func(connected bool)
}

func NewClient(host string) *Client { return &Client{host: host} }

func (c *Client) SetHost(host string) {
	c.mu.Lock()
	c.host = host
	c.mu.Unlock()
}

// Run is the connection loop; returns when ctx is done.
func (c *Client) Run(ctx interface {
	Done() <-chan struct{}
	Err() error
}, store *Store) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		c.mu.Lock()
		host := c.host
		c.mu.Unlock()
		if host == "" {
			if c.Connected != nil {
				c.Connected(false)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		conn, err := net.DialTimeout("tcp", host, 5*time.Second)
		if err != nil {
			if c.Connected != nil {
				c.Connected(false)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff += 2 * time.Second
			}
			continue
		}
		backoff = 2 * time.Second
		if c.Connected != nil {
			c.Connected(true)
		}
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 0, 1024), 4096)
		for sc.Scan() && ctx.Err() == nil {
			store.Decode(sc.Text())
		}
		conn.Close()
		if c.Connected != nil {
			c.Connected(false)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}
