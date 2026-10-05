package aprs

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Passcode derives the APRS-IS passcode for a callsign (base call,
// SSID ignored). The algorithm is public and identical everywhere
// (seed 0x73e2, even-index chars shifted into the high byte).
func Passcode(call string) int {
	if i := strings.IndexByte(call, '-'); i >= 0 {
		call = call[:i]
	}
	call = strings.ToUpper(strings.TrimSpace(call))
	hash := 0x73e2
	for i := 0; i < len(call); i++ {
		c := int(call[i])
		if i%2 == 0 {
			hash ^= c << 8
		} else {
			hash ^= c
		}
	}
	return hash & 0x7fff
}

// ISLine renders the TNC2 text form of a position packet for APRS-IS:
// "SRC>APRS,WIDE1-1,WIDE2-1,TCPIP*:!1357.33N/10033.71E>...".
func ISLine(src, dest, path, info string) string {
	p := strings.TrimSpace(path)
	if p != "" {
		p += ","
	}
	return fmt.Sprintf("%s>%s,%sTCPIP*:%s", src, dest, p, info)
}

// PostIS sends one packet to an APRS-IS server (e.g.
// rotate.aprs2.net:14580): one TCP connection per beacon — beacons
// are minutes apart, so a persistent feed buys nothing.
func PostIS(server, src, dest, path, info string) error {
	conn, err := net.DialTimeout("tcp", server, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if i := strings.LastIndex(server, ":"); i > 0 {
		_ = i
	}
	login := fmt.Sprintf("user %s pass %d vers SDRg35xx 1\r\n", src, Passcode(src))
	if _, err := conn.Write([]byte(login)); err != nil {
		return err
	}
	// Servers greet with comment lines; drain a little so the login is
	// processed before the packet, but do not insist on a logresp.
	buf := make([]byte, 512)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Read(buf)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write([]byte(ISLine(src, dest, path, info) + "\r\n"))
	return err
}
