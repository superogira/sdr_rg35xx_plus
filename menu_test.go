package main

import "testing"

// TestPageItemsCoverEveryPage: exactly one row list per page id, and
// the root page exposes one row per subpage — the Audio page row once
// went missing in a silent edit and the d-pad could never reach it.
func TestPageItemsCoverEveryPage(t *testing.T) {
	wantPages := []int{pageRoot, pageRx, pageFT8, pageSys, pageBM, pageAudio}
	if len(pageItems) != len(wantPages) {
		t.Fatalf("pageItems has %d pages, want %d — a page id exists with no row list (rows become unreachable)", len(pageItems), len(wantPages))
	}
	// Root: N subpages ⇒ N root rows, and row i opens page i+1.
	subpages := len(wantPages) - 1
	if got := len(pageItems[pageRoot]); got != subpages {
		t.Fatalf("root page has %d rows, want %d (= number of subpages)", got, subpages)
	}
	// Every non-root page has at least one row.
	for _, pg := range wantPages[1:] {
		if len(pageItems[pg]) == 0 {
			t.Fatalf("page %d has no rows", pg)
		}
	}
}
