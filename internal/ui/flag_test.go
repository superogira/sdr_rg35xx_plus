package ui

import (
	"bytes"
	"testing"
)

func TestRebaseViewBox(t *testing.T) {
	in := []byte(`<svg xmlns="x" viewBox="0 85.333 512 341.333"><rect y="85.334" width="512"/></svg>`)
	out := rebaseViewBox(in)
	if !bytes.Contains(out, []byte(`viewBox="0 0 512 341.333"`)) {
		t.Fatalf("viewBox not rebased: %s", out)
	}
	if !bytes.Contains(out, []byte(`<g transform="translate(-0,-85.333)">`)) {
		t.Fatalf("no shift group: %s", out)
	}
	if !bytes.Contains(out, []byte("</g></svg>")) {
		t.Fatalf("group not closed: %s", out)
	}
	// Zero-origin passes through untouched.
	zero := []byte(`<svg viewBox="0 0 10 10"><rect/></svg>`)
	if !bytes.Equal(rebaseViewBox(zero), zero) {
		t.Fatalf("zero-origin modified: %s", rebaseViewBox(zero))
	}
}

func TestEmbeddedFlagsAvailable(t *testing.T) {
	// The bundled set must serve a few known flags with no network and
	// no flagDir contents at all.
	for _, cc := range []string{"TH", "JP", "GB", "US", "BR"} {
		if GetFlag(cc, t.TempDir()) == nil {
			t.Errorf("embedded flag %s missing", cc)
		}
	}
}
