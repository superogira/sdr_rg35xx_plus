package main

import "testing"

// TestPageItemsCoverEveryPage: exactly one row list per page id, and
// the root page exposes one row per subpage — the Audio page row once
// went missing in a silent edit and the d-pad could never reach it.
func TestPageItemsCoverEveryPage(t *testing.T) {
	wantPages := []int{pageRoot, pageRx, pageAudio, pageDisp, pageADSB, pageGPS, pageAPRS, pageFT8, pageStation, pageSys}
	if len(pageItems) != len(wantPages) {
		t.Fatalf("pageItems has %d pages, want %d — a page id exists with no row list (rows become unreachable)", len(pageItems), len(wantPages))
	}
	// Root: N subpages + the Bookmarks row + the Exit row; subpage
	// rows open pages by position (the bookmarks row between Station
	// and System is handled before the positional dispatch), and the
	// last row is Exit.
	subpages := len(wantPages) - 1
	if got := len(pageItems[pageRoot]); got != subpages+2 {
		t.Fatalf("root page has %d rows, want %d (subpages + Bookmarks + Exit)", got, subpages+2)
	}
	if pageItems[pageRoot][subpages-1] != menuBM {
		t.Fatal("the row between Station and System must be Bookmarks")
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
		{menuHost, menuSample, menuFreq, menuStep, menuPPM, menuMode, menuGain, menuSQL, menuBW, menuDS, menuAGC},
		{menuAF, menuNR, menuHP, menuLP, menuLocalMute, menuVolume},
		{menuSpan, menuWFMin, menuWFMax},
		{menuADSBRadar, menuADSBLat, menuADSBLon, menuADSBHost, menuAISServer, menuAISRF, menuAISLog, menuClearMap},
		{menuGPSDev, menuGPSStat, menuGPSTime, menuGPSPos, menuGPSGrid, menuGPSAlt, menuGPSSpd, menuGPSCourse, menuGPSSats, menuGPSHdop, menuGPSAge, menuGPSFollow, menuGPSTimeSync},
		{menuAPRSRx, menuAPRSFreq, menuAPRSCall, menuAPRSBeacon, menuAPRSIS, menuAPRSServer, menuAPRSPath, menuAPRSSym, menuAPRSCmt, menuAPRSPre, menuAPRSLvl, menuAPRSStat, menuAPRSLog, menuAPRSIgate, menuAPRSGateLim, menuAPRSSrc, menuAPRSFixLat, menuAPRSFixLon, menuAPRSNow},
		{menuMap, menuFT8, menuBands, menuRTTY, menuRTTYLog, menuWefax, menuWefaxAuto, menuWefaxClear, menuCWDec, menuCWClear, menuSSTV, menuSSTVView, menuSSTVClear},
		{menuCall, menuGrid, menuAnt, menuRig, menuPSK},
		{menuWeb, menuWebPort, menuLang, menuSysMon, menuLogs, menuShot, menuUpdate},
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

// The root row mapping must stay exact: rows 0-7 open pages 1-8,
// row 8 is Bookmarks, row 9 is System, row 10 is Exit. An off-by-one
// here swapped System/Bookmarks and indexed a page that does not
// exist (index out of range crash).
func TestRootRowMapping(t *testing.T) {
	for sel := 0; sel < 8; sel++ {
		if got := rootRowPage(sel); got != sel+1 {
			t.Fatalf("row %d opens page %d, want %d", sel, got, sel+1)
		}
		if a := rootRowAction(sel, 0); a != rootPage {
			t.Fatalf("row %d action = %v, want rootPage", sel, a)
		}
	}
	if got := rootRowPage(9); got != pageSys {
		t.Fatalf("System row opens page %d, want pageSys", got)
	}
	if a := rootRowAction(8, menuBM); a != rootBookmarks {
		t.Fatal("bookmarks row must open the bookmark list")
	}
	if a := rootRowAction(10, menuExit); a != rootExit {
		t.Fatal("last row must exit")
	}
	if len(pageItems[pageRoot]) != 11 {
		t.Fatalf("root rows = %d, want 11", len(pageItems[pageRoot]))
	}
}
