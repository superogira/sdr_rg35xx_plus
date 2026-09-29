package adsb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegDBCacheLoad(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.txt")
	os.WriteFile(p, []byte("4CA87C=EI-EJG\n8851E4=\n"), 0644)
	d := NewRegDB(p)
	if r := d.Lookup("4ca87c"); r != "EI-EJG" {
		t.Fatalf("got %q", r)
	}
	if r := d.Lookup("8851E4"); r != "" || len(d.pending) != 0 {
		t.Fatalf("cached unknown refetched: %q %v", r, d.pending)
	}
}
