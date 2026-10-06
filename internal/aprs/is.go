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

// GateLine renders a decoded RF frame for APRS-IS as an IGate would:
// the heard digipeater path plus the q-construct qAR (received
// directly from the source over RF) and our own call. Internet-origin
// markers must never be re-gated - callers check FrameFromInternet.
func GateLine(f *Frame, igate string) string {
	path := ""
	for i, d := range f.Digis {
		if i > 0 {
			path += ","
		}
		path += d
	}
	if path != "" {
		path += ","
	}
	path += "qAR," + igate
	return fmt.Sprintf("%s>%s,%s:%s", f.Src, f.Dest, path, string(f.Info))
}

// FrameFromInternet reports whether a decoded frame already carries an
// APRS-IS marker (q-construct or TCPIP) - such frames came off the
// internet and must NOT be gated back onto it (loop).
func FrameFromInternet(f *Frame) bool {
	for _, d := range f.Digis {
		if len(d) >= 2 && d[0] == 'q' && (d[1] == 'A' || d[1] == 'a') {
			return true
		}
		if d == "TCPIP" || d == "TCPXX" {
			return true
		}
	}
	return false
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

// PostRawIS sends a pre-rendered TNC2 line (the IGate path).
func PostRawIS(server, src, line string) error {
	conn, err := net.DialTimeout("tcp", server, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	CRLF := string([]byte{13, 10})
	login := fmt.Sprintf("user %s pass %d vers SDRg35xx 1"+CRLF, src, Passcode(src))
	if _, err := conn.Write([]byte(login)); err != nil {
		return err
	}
	buf := make([]byte, 512)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Read(buf)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write([]byte(line + CRLF))
	return err
}
