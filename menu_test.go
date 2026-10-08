// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestPageItemsCoverEveryPage: exactly one row list per page id, and
// the root page exposes one row per subpage — the Audio page row once
// went missing in a silent edit and the d-pad could never reach it.
func TestPageItemsCoverEveryPage(t *testing.T) {
	rootPages := []int{pageRoot, pageRx, pageAudio, pageDisp, pageADSB, pageGPS, pageAPRS, pageFT8, pageStation, pageSys}
	wantPages := append(append([]int{}, rootPages...), pageFT8Sub) // sub-page lives in pageItems too
	if len(pageItems) != len(wantPages) {
		t.Fatalf("pageItems has %d pages, want %d — a page id exists with no row list (rows become unreachable)", len(pageItems), len(wantPages))
	}
	// Root: N subpages + the Bookmarks row + the Exit row; subpage
	// rows open pages by position (the bookmarks row between Station
	// and System is handled before the positional dispatch), and the
	// last row is Exit.
	subpages := len(rootPages) - 1
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
		{menuAF, menuNR, menuHP, menuLP, menuNRNN, menuLocalMute, menuVolume},
		{menuSpan, menuWFMin, menuWFMax},
		{menuADSBRadar, menuADSBLat, menuADSBLon, menuADSBRF, menuRTLSrv, menuRTLSrvPort, menuADSBHost, menuAISServer, menuAISRF, menuAISLog, menuClearMap},
		{menuGPSDev, menuGPSStat, menuGPSTime, menuGPSPos, menuGPSGrid, menuGPSAlt, menuGPSSpd, menuGPSCourse, menuGPSSats, menuGPSHdop, menuGPSAge, menuGPSFollow, menuGPSTimeSync},
		{menuAPRSRx, menuAPRSFreq, menuAPRSCall, menuAPRSBeacon, menuAPRSIS, menuAPRSServer, menuAPRSPath, menuAPRSSym, menuAPRSCmt, menuAPRSPre, menuAPRSLvl, menuAPRSStat, menuAPRSLog, menuAPRSIgate, menuAPRSGateLim, menuAPRSSrc, menuAPRSFixLat, menuAPRSFixLon, menuAPRSNow},
		{menuFT8Sub, menuRTTY, menuRTTYLog, menuWefax, menuWefaxAuto, menuWefaxClear, menuCWDec, menuCWClear, menuDeepCW, menuDeepCWThreads, menuDeepCWWindow, menuDeepCWClear, menuSSTV, menuSSTVView, menuSSTVClear},
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

// TestPageADSBRenderMatchesDispatch: the pageADSB rows RENDERED on
// screen must be in the same order as pageItems[pageADSB], because
// activateItem dispatches by row INDEX against pageItems — a drift lands
// every press on the neighbouring row's action (real bug: "Beast
// server" row toggled ADS-B RF). The render list lives inline in
// main()'s switch, so this test pins it at the source level.
func TestPageADSBRenderMatchesDispatch(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Skip("source not available")
	}
	s := string(src)

	// Dispatch list for pageADSB.
	d := strings.Index(s, "menuADSBRadar, menuADSBLat")
	if d < 0 {
		t.Fatal("pageItems[pageADSB] row not found in main.go")
	}
	line := s[d : strings.IndexByte(s[d:], '\n')+d]
	var dispatch []string
	for _, id := range strings.Split(line, ",") {
		id = strings.TrimSpace(id)
		id = strings.TrimSuffix(id, "},")
		id = strings.TrimSuffix(id, "}")
		if id != "" {
			dispatch = append(dispatch, id)
		}
	}
	if len(dispatch) != 11 {
		t.Fatalf("pageItems[pageADSB] has %d rows: %v", len(dispatch), dispatch)
	}
	// Render block: labels appear in source order between the case
	// marker and the next case.
	begin := strings.Index(s, "case pageADSB:")
	end := strings.Index(s[begin:], "case pageAPRS:") + begin
	block := s[begin:end]
	keyRe := regexp.MustCompile(`i18n\.T\("(m_[a-z]+)"\)`)
	keyToID := map[string]string{
		"m_adsbradar":  "menuADSBRadar",
		"m_adsblat":    "menuADSBLat",
		"m_adsblon":    "menuADSBLon",
		"m_adsbrf":     "menuADSBRF",
		"m_rtlsrv":     "menuRTLSrv",
		"m_rtlsrvport": "menuRTLSrvPort",
		"m_adsbhost":   "menuADSBHost",
		"m_aishost":    "menuAISServer",
		"m_aisrf":      "menuAISRF",
		"m_aislog":     "menuAISLog",
		"m_clearcache": "menuClearMap",
	}
	var render []string
	for _, m := range keyRe.FindAllStringSubmatch(block, -1) {
		if id, ok := keyToID[m[1]]; ok {
			render = append(render, id)
		}
	}
	if len(render) != len(dispatch) {
		t.Fatalf("rendered %d label(s) with known ids, dispatch has %d — unmapped new row?", len(render), len(dispatch))
	}
	for i := range dispatch {
		if render[i] != dispatch[i] {
			t.Fatalf("row %d: screen shows %s but pressing dispatches %s — render/pageItems drift (presses land on the wrong action!)", i, render[i], dispatch[i])
		}
	}
}

// TestPageFT8SubRenderMatchesDispatch: pin the FT8 sub-page's rendered
// label order to its dispatch list (same drift class as the pageADSB
// pin — presses land on the neighbouring row when they drift).
func TestPageFT8SubRenderMatchesDispatch(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Skip("source not available")
	}
	s := string(src)
	begin := strings.Index(s, "case pageFT8Sub:")
	end := strings.Index(s[begin:], "case pageStation:") + begin
	if begin < 0 || end <= begin {
		t.Fatal("pageFT8Sub render block not found")
	}
	keyRe := regexp.MustCompile(`i18n\.T\("(m_[a-z0-9]+)"\)`)
	keyToID := map[string]string{
		"m_ft8":        "menuFT8",
		"m_ft8sens":    "menuFT8Sens",
		"m_bands":      "menuBands",
		"m_map":        "menuMap",
		"m_ft8ts":      "menuFT8TS",
		"m_ft8tsdepth": "menuFT8TSDepth",
		"m_ft8tsth":    "menuFT8TSThreads",
		"m_ft8tsband":  "menuFT8TSBand",
	}
	var render []string
	for _, m := range keyRe.FindAllStringSubmatch(s[begin:end], -1) {
		if id, ok := keyToID[m[1]]; ok {
			// availability closures repeat the row's label in both
			// return branches — one row, one entry
			if n := len(render); n > 0 && render[n-1] == id {
				continue
			}
			render = append(render, id)
		}
	}
	dispatch := []string{"menuFT8", "menuFT8Sens", "menuBands", "menuMap", "menuFT8TS", "menuFT8TSDepth", "menuFT8TSThreads", "menuFT8TSBand"}
	if len(render) != len(dispatch) {
		t.Fatalf("rendered %d mapped rows, dispatch has %d", len(render), len(dispatch))
	}
	for i := range dispatch {
		if render[i] != dispatch[i] {
			t.Fatalf("row %d: screen shows %s but pressing dispatches %s", i, render[i], dispatch[i])
		}
	}
}
