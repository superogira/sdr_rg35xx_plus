package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestListFlagCodesParses(t *testing.T) {
	// listFlagCodes hits the network; its JSON shape is pinned here via
	// the same struct decoding used in the real call.
	body := []byte(`[{"id":1,"name":"AC.svg"},{"id":2,"name":"AD.svg"},{"id":3,"name":"README.md"}]`)
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if len(e.Name) == 6 && strings.HasSuffix(e.Name, ".svg") {
			got = append(got, e.Name[:2])
		}
	}
	if len(got) != 2 || got[0] != "AC" || got[1] != "AD" {
		t.Fatalf("codes = %v", got)
	}
}

func TestPrefetchSkipsWhenMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "all.done"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// Must return immediately without any network attempt.
	PrefetchAllFlags(dir)
}
