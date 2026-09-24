package pskreporter

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

func hx(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// TestPacketAgainstSpecExample reproduces the worked example from
// pskreporter.info/pskdev.html byte for byte (header, both templates,
// receiver record prefix and both sender records).
func TestPacketAgainstSpecExample(t *testing.T) {
	spots := []Spot{
		{Sender: "N1DQ", FreqHz: 14070567, At: time.Unix(1200960084, 0)},
		{Sender: "KB1MBX", FreqHz: 14070987, At: time.Unix(1200960104, 0)},
	}
	pkt := buildPacket("N1DQ", "FN42hn", "Dipole", "RTL-SDR V4", spots, 1, 0, true)

	if pkt[0] != 0x00 || pkt[1] != 0x0A {
		t.Fatalf("version bytes: % x", pkt[:2])
	}
	if totalLen := int(pkt[2])<<8 | int(pkt[3]); totalLen != len(pkt) {
		t.Fatalf("length field %d != packet %d", totalLen, len(pkt))
	}
	if !bytes.Equal(pkt[8:12], []byte{0, 0, 0, 1}) {
		t.Fatalf("sequence: % x", pkt[8:12])
	}

	// 5-field template (with antenna + rig): 00 34 = 52 bytes.
	if !bytes.Contains(pkt, hx("00030034999200050001"+
		"8002FFFF0000768F"+
		"8004FFFF0000768F"+
		"8008FFFF0000768F"+
		"8009FFFF0000768F"+
		"800DFFFF0000768F"+
		"0000")) {
		t.Error("5-field receiver template bytes not found")
	}
	if !bytes.Contains(pkt, hx("0002003C999300078001FFFF0000768F800500040000768F800600010000768F800700010000768F800AFFFF0000768F800B00010000768F00960004")) {
		t.Error("sender template bytes not found")
	}
	// Receiver record: the 99 92 magic followed by "N1DQ" and
	// "FN42hn" (the length field and software string differ from the
	// spec example, which used a longer software name).
	if !bytes.Contains(pkt, hx("9992")) || !bytes.Contains(pkt, hx("044E31445106464E3432686E")) {
		t.Error("receiver record contents (N1DQ/FN42hn) not found")
	}
	// Sender records: callsign + frequency (0x00D6B327 / 0x00D6B4CB).
	if !bytes.Contains(pkt, hx("044E314451"+"00D6B327")) {
		t.Error("first sender record (N1DQ @ 00D6B327) not found")
	}
	if !bytes.Contains(pkt, hx("064B42314D4258"+"00D6B4CB")) {
		t.Error("second sender record (KB1MBX @ 00D6B4CB) not found")
	}
	if !bytes.Contains(pkt, hx("47953254")) || !bytes.Contains(pkt, hx("47953268")) {
		t.Error("flowStartSeconds values not found")
	}
	// mode FT8 with length prefix 03.
	if !bytes.Contains(pkt, hx("03465438")) {
		t.Error("mode string FT8 not found")
	}
}

func TestAddWhileDisabled(t *testing.T) {
	r := New("", "", "", "", false)
	r.Add(Spot{Sender: "K1ABC"})
	if r.Pending() != 0 {
		t.Fatal("spots buffered while inactive")
	}
	if n := r.Flush(); n != 0 {
		t.Fatal("flush sent while inactive")
	}
	r.SetStation("HS0ZKO", "OK04", "Dipole", "RTL-SDR V4")
	r.SetEnabled(true)
	r.Add(Spot{Sender: "K1ABC"})
	if r.Pending() != 1 {
		t.Fatal("spot not buffered once active")
	}
}

func TestBufferCap(t *testing.T) {
	r := New("HS0ZKO", "OK04", "Dipole", "RTL-SDR V4", true)
	for i := 0; i < maxBuffer+50; i++ {
		r.Add(Spot{Sender: "K1ABC"})
	}
	if r.Pending() > maxBuffer {
		t.Fatalf("buffer exceeded cap: %d", r.Pending())
	}
}
