// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

// Package adsb: Mode S Beast TCP client and ADS-B (DF17/DF18)
// decoding — aircraft position (CPR), callsign, velocity — feeding a
// TTL store the radar screen renders.
package adsb

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// Beast frame types after the 0x1a sync byte.
const (
	beastModeAC     = 0x31 // 2-byte payload
	beastModeSShort = 0x32 // 7-byte Mode S
	beastModeSLong  = 0x33 // 14-byte Mode S (ADS-B lives here)
)

// BeastDecoder un-frames a Mode S Beast TCP stream (as served by
// dump1090/readsb on port 30005): frames are 0x1a <type> <6-byte
// MLAT> <1-byte signal> <payload>, and every 0x1a inside the mlat,
// signal and payload bytes is doubled.
type BeastDecoder struct {
	buf   []byte
	onMsg func(msg []byte, mlat uint64, sig int)
}

// NewBeastDecoder builds a framer; onMsg receives each Mode S message
// (payload only). Payloads are only valid until the callback returns.
func NewBeastDecoder(onMsg func(msg []byte, mlat uint64, sig int)) *BeastDecoder {
	return &BeastDecoder{onMsg: onMsg}
}

// Feed consumes raw bytes from the TCP stream.
func (d *BeastDecoder) Feed(data []byte) {
	d.buf = append(d.buf, data...)
	for {
		// Scan for the sync byte.
		i := 0
		for ; i < len(d.buf); i++ {
			if d.buf[i] == 0x1a {
				break
			}
		}
		if i > 0 {
			d.buf = d.buf[i:]
		}
		if len(d.buf) < 2 {
			return
		}
		typ := d.buf[1]
		if typ != beastModeAC && typ != beastModeSShort && typ != beastModeSLong {
			// Stray 0x1a not followed by a frame type — drop the sync.
			d.buf = d.buf[1:]
			continue
		}
		payloadLen := map[byte]int{beastModeAC: 2, beastModeSShort: 7, beastModeSLong: 14}[typ]
		need := 7 + payloadLen

		// Pull one frame with un-escaping; may come up short on
		// partial data (then keep the partial for the next Feed), or
		// stumble onto a new sync byte (producer restarted mid-frame;
		// rescan from there).
		var frame []byte
		src := 2
		for len(frame) < need && src < len(d.buf) {
			b := d.buf[src]
			if b == 0x1a {
				if src+1 >= len(d.buf) {
					// Escape half or the next frame's sync — keep the
					// raw buffer untouched and wait for more bytes (the
					// outer loop rescans from the frame's own sync).
					return
				}
				if d.buf[src+1] != 0x1a {
					// New frame started: drop the incomplete one.
					d.buf = d.buf[src:]
					frame = nil
					break
				}
				frame = append(frame, 0x1a)
				src += 2
				continue
			}
			frame = append(frame, b)
			src++
		}
		if frame == nil {
			continue // rescan from the new sync byte
		}
		if len(frame) < need {
			// Incomplete: keep the RAW bytes (re-building from the
			// un-escaped frame would corrupt literal 0x1a bytes) and
			// wait for more.
			d.buf = d.buf[:src]
			return
		}
		mlat := uint64(0)
		for _, b := range frame[0:6] {
			mlat = mlat<<8 | uint64(b)
		}
		sig := int(frame[6])
		payload := frame[7:]
		d.buf = d.buf[src:]
		if d.onMsg != nil {
			d.onMsg(payload, mlat, sig)
		}
	}
}

// Client connects to a Beast server and feeds a BeastDecoder until the
// context is cancelled, reconnecting with backoff.
type Client struct {
	mu   sync.Mutex
	host string

	Connected func(connected bool)
}

func NewClient(host string) *Client { return &Client{host: host} }

// SetHost retargets the next connection attempt.
func (c *Client) SetHost(host string) {
	c.mu.Lock()
	c.host = host
	c.mu.Unlock()
}

// Run is the connection loop; returns when ctx is done.
func (c *Client) Run(ctx context.Context, onMsg func(msg []byte, mlat uint64, sig int)) {
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
		dec := NewBeastDecoder(onMsg)
		buf := make([]byte, 16384)
		for ctx.Err() == nil {
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			n, err := conn.Read(buf)
			if n > 0 {
				dec.Feed(buf[:n])
			}
			if err != nil {
				conn.Close()
				if c.Connected != nil {
					c.Connected(false)
				}
				break
			}
		}
		conn.Close()
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

var _ = fmt.Sprintf
