// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package rtltcp

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

// Server fans one live IQ stream out to many rtl_tcp clients. The app
// already owns the dongle through its own (single-client) rtl_tcp; this
// in-process server republishes the exact bytes the radio is receiving so
// other hosts (a laptop running SDRSharp/gqrx/tar1090) can connect to the
// device's IP and watch the same band without taking the dongle away.
//
// Commands from clients are parsed (so their command stream stays in sync)
// but not applied: the shared dongle is tuned by the app, so the stream a
// client receives is always the app's current frequency and rate.
type Server struct {
	ln     net.Listener
	header [12]byte

	mu      sync.Mutex
	clients map[*srvConn]struct{}
	closed  bool
}

// srvConn is one connected client.
type srvConn struct {
	conn net.Conn
	ch   chan []byte // bounded; a client that cannot keep up is dropped
	once sync.Once
}

// NewServer listens on addr ("0.0.0.0:1235" for LAN access) and sends
// the given tuner type / gain count in the RTL0 handshake.
func NewServer(addr string, tunerType, gainCount int32) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{ln: ln, clients: map[*srvConn]struct{}{}}
	copy(s.header[0:4], "RTL0")
	binary.LittleEndian.PutUint32(s.header[4:8], uint32(tunerType))
	binary.LittleEndian.PutUint32(s.header[8:12], uint32(gainCount))
	go s.acceptLoop()
	return s, nil
}

// Addr returns the bound address (useful when the port was 0).
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Clients reports the number of connected clients.
func (s *Server) Clients() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.clients)
}

func (s *Server) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.add(c)
	}
}

func (s *Server) add(c net.Conn) {
	sc := &srvConn{conn: c, ch: make(chan []byte, 8)}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return
	}
	s.clients[sc] = struct{}{}
	s.mu.Unlock()
	go sc.run(s.header)
}

// Broadcast sends one IQ block to every client. A client whose queue is
// full is closed rather than allowed to stall the radio's read loop —
// the live stream matters more than any one slow consumer.
func (s *Server) Broadcast(b []byte) {
	if len(b) == 0 {
		return
	}
	// Copy once: every client's writer reads the same backing array.
	block := append([]byte(nil), b...)
	s.mu.Lock()
	var slow []*srvConn
	for sc := range s.clients {
		select {
		case sc.ch <- block:
		default:
			slow = append(slow, sc)
		}
	}
	for _, sc := range slow {
		delete(s.clients, sc)
	}
	s.mu.Unlock()
	for _, sc := range slow {
		sc.close()
	}
}

// Close stops accepting and drops every client.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	clients := make([]*srvConn, 0, len(s.clients))
	for sc := range s.clients {
		clients = append(clients, sc)
	}
	s.clients = map[*srvConn]struct{}{}
	s.mu.Unlock()
	s.ln.Close()
	for _, sc := range clients {
		sc.close()
	}
}

func (sc *srvConn) close() {
	sc.once.Do(func() {
		close(sc.ch)
		sc.conn.Close()
	})
}

// run writes the handshake, streams IQ from the queue, and drains
// incoming commands in parallel so a client's writes never block.
func (sc *srvConn) run(header [12]byte) {
	if _, err := sc.conn.Write(header[:]); err != nil {
		sc.close()
		return
	}
	go sc.readCommands()

	// Coalesce queued blocks into fewer, larger writes: rtl_tcp clients
	// (and their TCP stacks) prefer bigger reads, and it lets a lagging
	// consumer catch up in one shot.
	buf := make([]byte, 0, 64*1024)
	for {
		block, ok := <-sc.ch
		if !ok {
			return
		}
		buf = append(buf[:0], block...)
	coalesce:
		for len(buf) < 48*1024 {
			select {
			case more, ok := <-sc.ch:
				if !ok {
					sc.flush(buf)
					return
				}
				buf = append(buf, more...)
			default:
				break coalesce
			}
		}
		sc.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := sc.conn.Write(buf); err != nil {
			sc.close()
			return
		}
	}
}

func (sc *srvConn) flush(buf []byte) {
	if len(buf) == 0 {
		return
	}
	sc.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	sc.conn.Write(buf)
}

// readCommands consumes the 5-byte command frames so the client's socket
// send buffer never fills with unread commands. The parameters are
// ignored: the shared dongle obeys the app, not the remote client. On EOF
// (client half-close, e.g. a graceful FIN) it simply stops — there is
// nothing left to drain, and the write path notices when the peer goes.
func (sc *srvConn) readCommands() {
	var b [5]byte
	for {
		if _, err := io.ReadFull(sc.conn, b[:]); err != nil {
			return
		}
	}
}
