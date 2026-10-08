// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import (
	"bufio"
	"net"
	"strings"
	"testing"
)

// N0CALL -> 13023 is the universally quoted APRS-IS passcode vector.
func TestPasscode(t *testing.T) {
	if got := Passcode("N0CALL"); got != 13023 {
		t.Fatalf("Passcode(N0CALL) = %d, want 13023", got)
	}
	// SSID must be ignored (the passcode is for the base call).
	if Passcode("N0CALL-7") != 13023 {
		t.Fatal("SSID must not change the passcode")
	}
}

func TestISLine(t *testing.T) {
	l := ISLine("HS1ABC-7", "APRS", "WIDE1-1,WIDE2-1", "!1357.33N/10033.71E>hi")
	want := "HS1ABC-7>APRS,WIDE1-1,WIDE2-1,TCPIP*:!1357.33N/10033.71E>hi"
	if l != want {
		t.Fatalf("ISLine = %q, want %q", l, want)
	}
	if !strings.HasSuffix(ISLine("X", "APRS", "", "y"), "APRS,TCPIP*:y") {
		t.Fatal("empty path must still carry TCPIP*")
	}
}

// End-to-end against a fake APRS-IS server: login then packet must
// arrive well-formed.
func TestPostISFakeServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type res struct {
		login, packet string
	}
	ch := make(chan res, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		login, _ := r.ReadString('\n')
		// greet so the client's drain read completes fast
		c.Write([]byte("# fake-aprs-is server\r\n"))
		packet, _ := r.ReadString('\n')
		ch <- res{strings.TrimSpace(login), strings.TrimSpace(packet)}
	}()
	if err := PostIS(ln.Addr().String(), "N0CALL-7", "APRS", "WIDE1-1", "!1357.33N/10033.71E>"); err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if !strings.HasPrefix(r.login, "user N0CALL-7 pass 13023 vers ") {
		t.Errorf("login = %q", r.login)
	}
	if !strings.HasPrefix(r.packet, "N0CALL-7>APRS,WIDE1-1,TCPIP*:!1357.33N") {
		t.Errorf("packet = %q", r.packet)
	}
}

// The IGate must never forward internet-origin frames (loop) and must
// render the qAR construct with the heard path intact.
func TestGateLineAndInternetGuard(t *testing.T) {
	f := &Frame{Src: "HS1ABC-9", Dest: "APRS", Digis: []string{"WIDE1-1*", "WIDE2-1"},
		Info: []byte("!1357.33N/10033.71E>")}
	line := GateLine(f, "E25WOP-8")
	want := "HS1ABC-9>APRS,WIDE1-1*,WIDE2-1,qAR,E25WOP-8:!1357.33N/10033.71E>"
	if line != want {
		t.Fatalf("GateLine = %q want %q", line, want)
	}
	if FrameFromInternet(f) {
		t.Fatal("fresh RF frame flagged as internet")
	}
	f2 := &Frame{Src: "X", Dest: "APRS", Digis: []string{"qAR,SOMEGATE"}}
	if !FrameFromInternet(f2) {
		t.Fatal("qAR frame must be rejected")
	}
	f3 := &Frame{Src: "X", Dest: "APRS", Digis: []string{"TCPIP*"}}
	if !FrameFromInternet(f3) {
		t.Fatal("TCPIP frame must be rejected")
	}
}
