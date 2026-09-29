package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigKeysRoundTrip: every ADS-B/AIS key the app writes must also
// be READ back at boot. adsbhost/adsblat/adsblon were saved but never
// restored, so a hand-edited Beast server silently reverted to the
// default on every restart (including after an OTA update, which
// re-execs).
func TestConfigKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "sdrg35xx.ini")
	body := strings.Join([]string{
		"host=127.0.0.1:1",
		"adsbhost=192.168.9.99:30005",
		"adsblat=13.70000",
		"adsblon=100.70000",
		"adsblayer=2",
		"aishost=192.168.8.88:29420",
		"beasthosts=192.168.9.99:30005,10.0.0.5:30005",
		"aishosts=192.168.8.88:29420,10.0.0.6:29420",
	}, "\n")
	if err := os.WriteFile(ini, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := readIni(ini)
	for _, k := range []string{"adsbhost", "adsblat", "adsblon", "adsblayer", "aishost", "beasthosts", "aishosts"} {
		if cfg[k] == "" {
			t.Fatalf("key %q not parsed from ini", k)
		}
	}
	// The values must be exactly what the user typed (host strings are
	// not reformatted, so the reconnect target is preserved).
	if cfg["adsbhost"] != "192.168.9.99:30005" {
		t.Fatalf("adsbhost = %q", cfg["adsbhost"])
	}
	if cfg["aishost"] != "192.168.8.88:29420" {
		t.Fatalf("aishost = %q", cfg["aishost"])
	}
	// And every key we persist must have a read path in the source.
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for _, k := range []string{"adsbhost", "adsblat", "adsblon", "adsblayer", "aishost", "beasthosts", "aishosts"} {
		if !strings.Contains(s, `cfg["`+k+`"]`) {
			t.Fatalf("key %q never read in main.go", k)
		}
		reads := strings.Count(s, `if v, ok := cfg["`+k+`"]`) + strings.Count(s, `if v, ok := cfg["`+k+`"];`)
		if reads == 0 {
			t.Fatalf("key %q has no read path (only the empty-check seed)", k)
		}
	}
}
