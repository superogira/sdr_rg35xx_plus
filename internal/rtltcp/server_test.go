package rtltcp

import (
	"io"
	"net"
	"testing"
	"time"
)

// TestServerFanout: two clients connect, both receive the RTL0 handshake
// and the same broadcast IQ bytes.
func TestServerFanout(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", 5, 29)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	clients := make([]net.Conn, 2)
	for i := range clients {
		c, err := net.Dial("tcp", s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients[i] = c
	}
	// Read the 12-byte handshake from each.
	for i, c := range clients {
		var hdr [12]byte
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			t.Fatalf("client %d handshake: %v", i, err)
		}
		if string(hdr[0:4]) != "RTL0" {
			t.Fatalf("client %d magic %q", i, hdr[0:4])
		}
	}
	if s.Clients() != 2 {
		t.Fatalf("Clients() = %d, want 2", s.Clients())
	}

	// Broadcast repeatedly and confirm both clients see the bytes (a
	// single block may still be coalescing in the writer).
	block := make([]byte, 4096)
	for i := range block {
		block[i] = byte(i)
	}
	got := make([][]byte, 2)
	done := make(chan struct{}, 2)
	for i, c := range clients {
		go func(i int, c net.Conn) {
			buf := make([]byte, len(block))
			c.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, err := io.ReadFull(c, buf); err != nil {
				t.Errorf("client %d read: %v", i, err)
			}
			got[i] = buf
			done <- struct{}{}
		}(i, c)
	}
	for i := 0; i < 40; i++ {
		s.Broadcast(block)
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		<-done
	}
	for i := range got {
		if len(got[i]) != len(block) || got[i][0] != block[0] || got[i][4095] != block[4095] {
			t.Fatalf("client %d data mismatch: %d bytes", i, len(got[i]))
		}
	}
}

// TestServerCommandsDrained: a client that sends rtl_tcp commands (as
// SDRSharp does on connect) must not block the broadcast path.
func TestServerCommandsDrained(t *testing.T) {
	s, err := NewServer("127.0.0.1:0", 5, 29)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var hdr [12]byte
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		t.Fatal(err)
	}
	// SetFrequency + SetSampleRate + SetFreqCorrection frames.
	for _, cmd := range []byte{0x01, 0x02, 0x05} {
		frame := []byte{cmd, 0, 0, 0, 0}
		if _, err := c.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	// Broadcast must still deliver.
	block := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	for i := 0; i < 20; i++ {
		s.Broadcast(block)
		time.Sleep(5 * time.Millisecond)
	}
	buf := make([]byte, len(block))
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read after commands: %v", err)
	}
}