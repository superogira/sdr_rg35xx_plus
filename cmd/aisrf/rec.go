package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

// recordMode captures raw CU8 IQ to a file for offline comparison
// against the reference AIS-catcher build.
func recordMode(host, path string, secs int) {
	c, err := net.Dial("tcp", host)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	hdr := make([]byte, 12)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	c.Read(hdr)
	c.SetReadDeadline(time.Time{})
	send := func(cmd byte, v uint32) {
		b := []byte{cmd, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		c.Write(b)
	}
	send(0x01, 162000000)
	f, err := os.Create(path)
	if err != nil {
		fmt.Println("create:", err)
		os.Exit(1)
	}
	defer f.Close()
	buf := make([]byte, 65536)
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	n := 0
	for time.Now().Before(deadline) {
		m, err := c.Read(buf)
		if err != nil {
			break
		}
		f.Write(buf[:m])
		n += m
	}
	fmt.Printf("recorded %d bytes (%.1f s at 256 ksps) -> %s\n", n, float64(n)/512000.0, path)
}
