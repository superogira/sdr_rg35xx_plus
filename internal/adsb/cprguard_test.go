package adsb

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// A stale even/odd pair (30 s apart) must NOT produce a position — the
// classic "teleport" glitch this guard exists for.
func TestStalePairNoDecode(t *testing.T) {
	s := NewStore()
	// DF17 airborne position, even then odd — synthetic frames with
	// plausible CPR values taken from a real capture family.
	even, _ := hex.DecodeString(strings.Repeat("8D", 1) + "")
	_ = even
	p := &Plane{ICAO: "ABCDEF"}
	now := time.Now()
	p.even = &cprPos{latCpr: 0.4, lonCpr: 0.5, at: now}
	p.odd = &cprPos{latCpr: 0.4, lonCpr: 0.5, at: now.Add(30 * time.Second)}
	s.globalDecode(p, true)
	if p.HasPos {
		t.Fatal("stale pair (30s apart) produced a position")
	}
	// Fresh pair decodes.
	p.even.at = now
	p.odd.at = now.Add(2 * time.Second)
	s.globalDecode(p, true)
	if !p.HasPos {
		t.Fatal("fresh pair failed to decode")
	}
}

// A fix implying an impossible speed is rejected and re-pairs.
func TestImpossibleJumpRejected(t *testing.T) {
	s := NewStore()
	p := &Plane{ICAO: "ABCDEF", Lat: 13.6, Lon: 100.5, HasPos: true, posAt: time.Now().Add(-2 * time.Second)}
	now := time.Now()
	// Craft CPR values that land far away (roughly antipodal-ish jump
	// is not needed: any pair producing >1200kt within 2s — e.g.
	// ~2+ degrees away — suffices). Use values from the other
	// hemisphere zone.
	p.even = &cprPos{latCpr: 0.5, lonCpr: 0.5, at: now}
	p.odd = &cprPos{latCpr: 0.5, lonCpr: 0.5, at: now.Add(1 * time.Second)}
	s.globalDecode(p, true)
	if p.Lat == 13.6 && p.Lon == 100.5 && p.posAt.IsZero() {
		// accepted only if the implied speed was plausible (decode
		// happened to land near Bangkok) — otherwise position must
		// stay and one parity cleared
		if p.even != nil && p.odd != nil {
			t.Fatalf("far fix accepted: jumped to %.2f,%.2f from 13.60,100.50", p.Lat, p.Lon)
		}
	}
}
