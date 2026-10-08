// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package ais

import "testing"

func TestMMSIReference(t *testing.T) {
	cases := map[string]string{
		"!AIVDM,1,1,,B,15M67FC000G?ufbE`FepT@3n00Sa,0*5C": "366053209",
		"!AIVDM,1,1,,A,13u?etPv2;0n:dDPwUM1U1Cb069D,0*23": "265547250",
	}
	for line, want := range cases {
		s := NewStore()
		s.Decode(line)
		if _, ok := s.ships[want]; !ok {
			for k := range s.ships {
				t.Errorf("%s: got %s want %s", line, k, want)
			}
		}
	}
}

func TestBadChecksumRejected(t *testing.T) {
	s := NewStore()
	s.Decode("!AIVDM,1,1,,B,15M67FC000G?ufbE`FepT@3n00Sb,0*5C")
	if len(s.ships) != 0 {
		t.Fatalf("corrupt sentence accepted: %v", s.ships)
	}
}
