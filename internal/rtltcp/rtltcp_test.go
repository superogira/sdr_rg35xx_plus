package rtltcp

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeServer accepts one connection, sends the dongle header, streams
// filler IQ, and reports when its command reader sees EOF.
func fakeServer(t *testing.T) (addr string, sawEOF chan struct{}, accepted2 chan net.Conn) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sawEOF = make(chan struct{}, 1)
	accepted2 = make(chan net.Conn, 1)
	go func() {
		serve := func(conn net.Conn) (eof bool) {
			// 12-byte standard header: RTL0 + tuner + gain count (little-endian).
			var hdr [12]byte
			copy(hdr[0:4], "RTL0")
			binary.LittleEndian.PutUint32(hdr[4:8], 1)
			binary.LittleEndian.PutUint32(hdr[8:12], 29)
			conn.Write(hdr[:])
			// Stream filler IQ like a real server does, continuously.
			go func() {
				buf := make([]byte, 65536)
				for i := range buf {
					buf[i] = byte(i)
				}
				for {
					if _, err := conn.Write(buf); err != nil {
						return
					}
				}
			}()
			// Command reader: blocks until the client FIN arrives.
			one := make([]byte, 1)
			_, err := conn.Read(one)
			conn.Close()
			return err == io.EOF
		}
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if !serve(conn) {
			return
		}
		close(sawEOF)
		// Back to accept, like rtl_tcp's single-client loop.
		c2, err := ln.Accept()
		if err == nil {
			serve(c2)
			accepted2 <- c2
		}
		ln.Close()
	}()
	return ln.Addr().String(), sawEOF, accepted2
}

// TestCloseGracefulSendsFIN: with the server streaming hard, the client
// disconnect must deliver a FIN (server read → EOF) promptly — not an
// RST from closing with a full receive buffer — and the server must be
// back in its accept state for the next client.
func TestCloseGracefulSendsFIN(t *testing.T) {
	addr, sawEOF, accepted2 := fakeServer(t)

	c, err := Dial(addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// Read a little so the session looks "live" with backlog piling up.
	buf := make([]byte, 16384)
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	c.ReadIQ(buf)

	done := make(chan struct{})
	go func() {
		c.CloseGraceful()
		close(done)
	}()

	select {
	case <-sawEOF:
		// The FIN reached the server's command reader.
	case <-time.After(3 * time.Second):
		t.Fatal("server never saw EOF — CloseGraceful did not send a clean FIN")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseGraceful did not return (drain stuck)")
	}
	// The very next client must get served: proof the teardown left the
	// server ready to accept instead of wedged.
	c2, err := Dial(addr, 3*time.Second)
	if err != nil {
		t.Fatalf("immediate reconnect failed: %v — server left wedged?", err)
	}
	c2.CloseGraceful()
	select {
	case <-accepted2:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not return to accept after graceful close")
	}
}
