// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
	"time"
)

// cwFeed models the rolling decoder text the way production sees it:
// characters are APPENDED (the whole buffer is re-read each poll).
type cwFeed struct{ s string }

func (f *cwFeed) app(x string) string { f.s += x; return f.s }

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCWSpotterHarvest: incremental "DE <call>" extraction — growth,
// partial words, DE split across polls, cooldown, own call, junk.
func TestCWSpotterHarvest(t *testing.T) {
	s := newCWSpotter()
	var f cwFeed

	// Partial callsign: not reported yet.
	if got := s.harvest(0, f.app("CQ CQ DE E25"), "HS0ABC"); !eq(got, nil) {
		t.Fatalf("partial call reported: %v", got)
	}
	// The space-terminated word completes it (DE was held from the
	// previous poll — the pair survives the chunk boundary).
	if got := s.harvest(0, f.app("WOP E25WOP"), "HS0ABC"); !eq(got, []string{"E25WOP"}) {
		t.Fatalf("full call not extracted: %v", got)
	}
	// Repetition within the cooldown is suppressed.
	if got := s.harvest(0, f.app(" K"), "HS0ABC"); !eq(got, nil) {
		t.Fatalf("repeat not suppressed: %v", got)
	}

	// Neural channel shares the cooldown map; own call is skipped.
	if got := s.harvest(1, "e25wop de hs0abc 599 bk", "HS0ABC"); !eq(got, nil) {
		t.Fatalf("neural dup/own call reported: %v", got)
	}

	// A question mark ends a word: "HS5ABCQR?" is reportable.
	if got := s.harvest(0, f.app(" QRZ TEST 5NN DE HS5ABCQR?"), "HS0ABC"); !eq(got, []string{"HS5ABCQR"}) {
		t.Fatalf("?-terminated sender wrong: %v", got)
	}

	// Junk tokens after DE are skipped.
	if got := s.harvest(0, f.app(" DE HELLO DE 123 DE E25WOP/ K"), "HS0ABC"); !eq(got, nil) {
		t.Fatalf("junk after DE reported: %v", got)
	}

	// Head trim (rolling buffer shrank): offset resets, re-scan stays
	// quiet thanks to the cooldown.
	if got := s.harvest(0, "CQ DE E25WOP K", "HS0ABC"); !eq(got, nil) {
		t.Fatalf("after trim, cooldown broken: %v", got)
	}

	// After the cooldown the same call is reportable again.
	s.last["E25WOP"] = time.Now().Add(-cwSpotCooldown - time.Second)
	if got := s.harvest(0, "CQ DE E25WOP K DE E25WOP K", "HS0ABC"); !eq(got, []string{"E25WOP"}) {
		t.Fatalf("post-cooldown call not reported: %v", got)
	}
}

// TestCWSpotterMultiWord: several senders in one fresh chunk.
func TestCWSpotterMultiWord(t *testing.T) {
	s := newCWSpotter()
	got := s.harvest(0, "DE JA1ABC DE W9XYZ DE G4ABC K", "")
	want := []string{"JA1ABC", "W9XYZ", "G4ABC"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("multi-sender extraction: got %v want %v", got, want)
	}
}
