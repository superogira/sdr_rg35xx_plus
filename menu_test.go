package main

import "testing"

// TestPageItemsCoverEveryPage: exactly one row list per page id, and
// the root page exposes one row per subpage — the Audio page row once
// went missing in a silent edit and the d-pad could never reach it.
func TestPageItemsCoverEveryPage(t *testing.T) {
	wantPages := []int{pageRoot, pageRx, pageFT8, pageSys, pageBM, pageAudio, pageADSB}
	if len(pageItems) != len(wantPages) {
		t.Fatalf("pageItems has %d pages, want %d — a page id exists with no row list (rows become unreachable)", len(pageItems), len(wantPages))
	}
	// Root: N subpages + the Exit row; row i opens page i+1, the last
	// row is Exit (handled before the positional dispatch).
	subpages := len(wantPages) - 1
	if got := len(pageItems[pageRoot]); got != subpages+1 {
		t.Fatalf("root page has %d rows, want %d (subpages + Exit)", got, subpages+1)
	}
	if pageItems[pageRoot][len(pageItems[pageRoot])-1] != menuExit {
		t.Fatal("root page last row must be Exit")
	}
	// Every non-root page has at least one row.
	for _, pg := range wantPages[1:] {
		if len(pageItems[pg]) == 0 {
			t.Fatalf("page %d has no rows", pg)
		}
	}
}

// TestRootRowsOpenMatchingPages: root row i must open page i+1, so the
// ROW LISTS must be in the same order as the page consts. A drift here
// swaps whole subpages (Audio↔FT8 was a real bug).
func TestRootRowsOpenMatchingPages(t *testing.T) {
	want := [][]int{
		{menuHost, menuSample, menuFreq, menuPPM, menuMode, menuGain, menuSQL, menuBW, menuDS, menuAGC, menuSpan, menuStep, menuWFMin, menuWFMax},
		{menuAF, menuNR, menuHP, menuLP, menuLocalMute},
		{menuADSBLat, menuADSBLon, menuADSBHost, menuAISServer, menuAISRF, menuAISLog, menuADSBRadar, menuClearMap},
		{menuFT8, menuBands, menuCall, menuGrid, menuAnt, menuRig, menuPSK, menuMap, menuRTTY, menuRTTYLog},
		{menuBM},
		{menuWeb, menuWebPort, menuLang, menuSysMon, menuLogs, menuVolume, menuShot, menuUpdate},
	}
	for i, w := range want {
		got := pageItems[pageRoot+1+i]
		if len(got) != len(w) {
			t.Fatalf("page %d has %d rows, want %d", pageRoot+1+i, len(got), len(w))
		}
		for j := range w {
			if got[j] != w[j] {
				t.Fatalf("page %d row %d = %d, want %d (subpages swapped?)", pageRoot+1+i, j, got[j], w[j])
			}
		}
	}
}
