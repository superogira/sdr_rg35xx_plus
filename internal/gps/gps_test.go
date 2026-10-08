// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package gps

import (
	"strings"
	"testing"
	"time"
)

// Real-world shaped sentences (u-blox style talker $GP/$GN mix) with
// valid checksums.
func withSum(body string) string {
	var sum byte
	for i := 0; i < len(body); i++ {
		sum ^= body[i]
	}
	return "$" + body + "*" + hexByte(sum)
}

func hexByte(b byte) string {
	const d = "0123456789ABCDEF"
	return string([]byte{d[b>>4], d[b&0xF]})
}

func TestGGAFix(t *testing.T) {
	r := New()
	// 1347.0231 N = 13°47.0231' = 13.78372°, 10035.5123 E = 100°35.5123' = 100.59187°
	r.Feed(withSum("GPGGA,064732.00,1347.0231,N,10035.5123,E,1,09,0.85,12.4,M,0.0,M,,"))
	f := r.Snapshot()
	if !f.Valid || f.Quality != 1 {
		t.Fatalf("no fix: %+v", f)
	}
	if d := f.Lat - 13.783718; d > 1e-5 || d < -1e-5 {
		t.Fatalf("lat %.6f", f.Lat)
	}
	if d := f.Lon - 100.591872; d > 1e-5 || d < -1e-5 {
		t.Fatalf("lon %.6f", f.Lon)
	}
	if f.SatsUsed != 9 || f.Alt != 12.4 || f.HDOP != 0.85 {
		t.Fatalf("gga fields: %+v", f)
	}
	if time.Since(f.Updated) > 2*time.Second {
		t.Fatalf("stale updated")
	}
}

func TestRMCSpeedCourse(t *testing.T) {
	r := New()
	r.Feed(withSum("GPRMC,064733.00,A,1347.0231,N,10035.5123,E,0.55,92.12,041026,,,A"))
	f := r.Snapshot()
	if !f.Valid || f.SpeedKt != 0.55 || f.CourseDeg != 92.12 {
		t.Fatalf("rmc: %+v", f)
	}
	if f.TimeUTC != "064733.00" || f.DateUTC != "041026" {
		t.Fatalf("rmc time/date: %q %q", f.TimeUTC, f.DateUTC)
	}
	if l := TimeLabel(f); l != "06:47:33 · 04/10/26 UTC" {
		t.Fatalf("time label %q", l)
	}
	// Status V drops the fix flag.
	r.Feed(withSum("GPRMC,064800.00,V,1347.0231,N,10035.5123,E,,,041026,,,N"))
	if r.Snapshot().Valid {
		t.Fatalf("V must invalidate")
	}
}

func TestGSASatsAndGSVView(t *testing.T) {
	r := New()
	r.Feed(withSum("GPGSA,A,3,22,14,01,,,,,,,,,,1.7,0.85,1.5*"))
	// (checksum recomputed below — build via withSum without trailing *)
	r2 := New()
	r2.Feed(withSum("GPGSA,A,3,22,14,01,,,,,,,,,,1.7,0.85,1.5"))
	if got := r2.Snapshot().SatsUsed; got != 3 {
		t.Fatalf("sats used %d", got)
	}
	r2.Feed(withSum("GPGSV,3,1,11,03,03,111,00,04,15,270,30,06,01,010,00,13,06,292,32"))
	if got := r2.Snapshot().SatsView; got != 11 {
		t.Fatalf("sats in view %d", got)
	}
}

func TestChecksumRejectsCorruption(t *testing.T) {
	r := New()
	r.Feed(withSum("GPGGA,064732.00,1347.0231,N,10035.5123,E,1,09,0.85,12.4,M,0.0,M,,"))
	before := r.Snapshot().Lat
	bad := withSum("GPGGA,064732.00,9999.9999,N,10035.5123,E,1,09,0.85,12.4,M,0.0,M,,")
	bad = bad[:len(bad)-2] + "FF" // wrong checksum
	r.Feed(bad)
	if r.Snapshot().Lat != before {
		t.Fatalf("corrupt line accepted")
	}
}

func TestMaidenhead(t *testing.T) {
	// Bangkok-ish 13.78, 100.59 → OK03ev (4-char OK03 is the safe part).
	g := Maidenhead(13.783718, 100.591872)
	if !strings.HasPrefix(g, "OK03") || len(g) != 6 {
		t.Fatalf("grid %q", g)
	}
	if Maidenhead(0, 0) != "JJ00aa" {
		t.Fatalf("origin %q", Maidenhead(0, 0))
	}
}

func TestGGAZeroQualityInvalidates(t *testing.T) {
	r := New()
	r.Feed(withSum("GPGGA,064732.00,1347.0231,N,10035.5123,E,1,09,0.85,12.4,M,0.0,M,,"))
	r.Feed(withSum("GPGGA,064900.00,1347.0231,N,10035.5123,E,0,00,,,M,0.0,M,,"))
	f := r.Snapshot()
	if f.Valid {
		t.Fatalf("quality 0 must invalidate")
	}
	if f.Lat == 0 {
		t.Fatalf("last position should persist")
	}
}
