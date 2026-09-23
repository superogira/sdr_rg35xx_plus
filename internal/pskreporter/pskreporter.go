// Package pskreporter formats and sends FT8 reception reports to
// PSK Reporter (report.pskreporter.info:4739) using the IPFIX-style
// "cookie cutter" layout from pskreporter.info/pskdev.html.
//
// Sender template chosen: senderCallsign, frequency(4B), sNR(1B),
// iMD(1B), mode, informationSource(1B), flowStartSeconds — i.e. the
// 0x99 0x93 / 0x00 0x07 descriptor (we know the SNR but never the
// sender's locator).
package pskreporter

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"
)

// Spot is one reception to report.
type Spot struct {
	Sender string // callsign heard
	FreqHz uint32 // actual RF frequency of the transmission
	SNRDb  int8   // received SNR (clamped to -128..127)
	At     time.Time
}

const (
	dest       = "report.pskreporter.info:4739"
	maxBuffer  = 200
	maxPerPkt  = 40
	softwareID = "SDRg35xx"
)

// Reporter buffers spots and flushes them over UDP.
type Reporter struct {
	mu       sync.Mutex
	call     string
	grid     string
	enabled  bool
	spots    []Spot
	seq      uint32
	sessID   uint32
	sentPkts int
	logf     func(format string, a ...any)
}

// New creates a reporter; call/gridsquare may be empty (reporting
// stays inactive until both are set and enabled).
func New(call, grid string, enabled bool) *Reporter {
	return &Reporter{
		call:    call,
		grid:    grid,
		enabled: enabled,
		sessID:  rand.Uint32(),
		logf:    func(format string, a ...any) { fmt.Fprintf(os.Stderr, format, a...) },
	}
}

// SetStation updates the receiver identity.
func (r *Reporter) SetStation(call, grid string) {
	r.mu.Lock()
	r.call, r.grid = call, grid
	r.mu.Unlock()
}

// SetEnabled turns reporting on/off.
func (r *Reporter) SetEnabled(on bool) {
	r.mu.Lock()
	r.enabled = on
	r.mu.Unlock()
}

// Add buffers one spot (ignored while inactive; oldest dropped at the
// cap so a long offline stretch cannot grow without bound).
func (r *Reporter) Add(s Spot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.enabled || r.call == "" || r.grid == "" {
		return
	}
	r.spots = append(r.spots, s)
	if len(r.spots) > maxBuffer {
		r.spots = r.spots[len(r.spots)-maxBuffer:]
	}
}

// Pending reports the buffered spot count (diagnostics).
func (r *Reporter) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spots)
}

// Flush sends at most maxPerPkt buffered spots in one datagram,
// including the format descriptors for the first three packets.
// Returns the number of spots sent.
func (r *Reporter) Flush() int {
	r.mu.Lock()
	if !r.enabled || r.call == "" || r.grid == "" || len(r.spots) == 0 {
		r.mu.Unlock()
		return 0
	}
	n := maxPerPkt
	if len(r.spots) < n {
		n = len(r.spots)
	}
	batch := r.spots[:n]
	r.spots = r.spots[n:]
	call, grid := r.call, r.grid
	r.seq += uint32(n)
	sent := r.sentPkts
	r.sentPkts++
	r.mu.Unlock()

	pkt := buildPacket(call, grid, batch, r.seq, r.sessID, sent < 3)
	addr, err := net.ResolveUDPAddr("udp", dest)
	if err != nil {
		r.logf("psk: resolve: %v\n", err)
		return 0
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		r.logf("psk: dial: %v\n", err)
		return 0
	}
	defer conn.Close()
	if _, err := conn.Write(pkt); err != nil {
		r.logf("psk: send: %v\n", err)
		return 0
	}
	r.logf("psk: sent %d spots (%d bytes)\n", n, len(pkt))
	return n
}

// buildPacket assembles one complete datagram.
func buildPacket(call, grid string, spots []Spot, seq, sess uint32, withTemplates bool) []byte {
	var body []byte // everything after the 16-byte header

	if withTemplates {
		// Receiver template: receiverCallsign, receiverLocator,
		// decodingSoftware (variable-length strings).
		body = append(body, 0x00, 0x03, 0x00, 0x24, 0x99, 0x92, 0x00, 0x03,
			0x00, 0x01,
			0x80, 0x02, 0xFF, 0xFF, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x04, 0xFF, 0xFF, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x08, 0xFF, 0xFF, 0x00, 0x00, 0x76, 0x8F,
			0x00, 0x00)
		// Sender template: senderCallsign, frequency(4B), sNR(1B),
		// iMD(1B), mode, informationSource(1B), flowStartSeconds(4B).
		body = append(body, 0x00, 0x02, 0x00, 0x3C, 0x99, 0x93, 0x00, 0x07,
			0x80, 0x01, 0xFF, 0xFF, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x05, 0x00, 0x04, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x06, 0x00, 0x01, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x07, 0x00, 0x01, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x0A, 0xFF, 0xFF, 0x00, 0x00, 0x76, 0x8F,
			0x80, 0x0B, 0x00, 0x01, 0x00, 0x00, 0x76, 0x8F,
			0x00, 0x96, 0x00, 0x04)
	}

	// Receiver information record (99 92): three length-prefixed
	// strings, null padded to a multiple of 4.
	var rx []byte
	rx = appendLenString(rx, call)
	rx = appendLenString(rx, grid)
	rx = appendLenString(rx, softwareID)
	for len(rx)%4 != 0 {
		rx = append(rx, 0)
	}
	body = append(body, 0x99, 0x92)
	body = binary.BigEndian.AppendUint16(body, uint16(len(rx)+4))
	body = append(body, rx...)

	// Sender information records (99 93).
	var tx []byte
	for _, s := range spots {
		tx = appendLenString(tx, s.Sender)
		tx = binary.BigEndian.AppendUint32(tx, s.FreqHz)
		tx = append(tx, byte(s.SNRDb)) // sNR
		tx = append(tx, 0)             // iMD unknown
		tx = appendLenString(tx, "FT8")
		tx = append(tx, 1) // informationSource: automatically extracted
		tx = binary.BigEndian.AppendUint32(tx, uint32(s.At.Unix()))
	}
	for len(tx)%4 != 0 {
		tx = append(tx, 0)
	}
	body = append(body, 0x99, 0x93)
	body = binary.BigEndian.AppendUint16(body, uint16(len(tx)+4))
	body = append(body, tx...)

	// Header: version 10, total length, time, sequence, session id.
	var pkt []byte
	pkt = append(pkt, 0x00, 0x0A)
	pkt = binary.BigEndian.AppendUint16(pkt, uint16(len(body)+16))
	pkt = binary.BigEndian.AppendUint32(pkt, uint32(time.Now().Unix()))
	pkt = binary.BigEndian.AppendUint32(pkt, seq)
	pkt = binary.BigEndian.AppendUint32(pkt, sess)
	return append(pkt, body...)
}

func appendLenString(dst []byte, s string) []byte {
	dst = append(dst, byte(len(s)))
	return append(dst, s...)
}
