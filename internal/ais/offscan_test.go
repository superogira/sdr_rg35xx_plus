package ais

import (
	"strings"
	"testing"
)

func TestOffsetScan(t *testing.T) {
	payload := "55?MbV02;H;s<HtKR20EHE:0@T4@Dn222222220t1EHE:4;0@T4@Dn00000000000" + "00000000000"
	p := make([]byte, 0, len(payload))
	for k := 0; k < len(payload); k++ {
		c := payload[k]
		v := c - 48
		if v > 40 {
			v -= 8
		}
		p = append(p, v)
	}
	for off := 70; off <= 240; off += 2 {
		name := text(p, off, 120)
		name = strings.TrimSpace(name)
		if len(name) > 8 && isMostlyPrintable(name) {
			t.Logf("off=%d: %q", off, name)
		}
	}
}

func isMostlyPrintable(s string) bool {
	n := 0
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' {
			n++
		}
	}
	return n*2 > len(s)
}
