// sdr35 — an RTL-SDR receiver (rtl_tcp client) for the Anbernic RG35XX.
//
// Top: 640-wide scrolling spectrum waterfall (±120 kHz around the tuned
// frequency). Bottom: frequency, mode, signal level, status. Audio is FM-
// demodulated in-process and piped to aplay/mpv (see internal/audio).
//
// Controls (RG35XX pad):
//
//	←/→   tune down/up by step     ↑/↓  tune ±10× step
//	A     cycle WFM/NFM            X    cycle gain (AGC → 0…max)
//	L1/R1 volume −/+               Y    FT8 slot sync (FT8 on)
//	SELECT FT8 history · MENU settings (hold 3 s = exit)
//	MENU+START together = screenshot (any screen)
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sdr35/internal/adsb"
	"sdr35/internal/ais"
	"sdr35/internal/aprs"
	"sdr35/internal/audio"
	"sdr35/internal/backlight"
	"sdr35/internal/deepcw"
	"sdr35/internal/dsp"
	"sdr35/internal/ft8ts"
	"sdr35/internal/geo"
	"sdr35/internal/gps"
	"sdr35/internal/i18n"
	"sdr35/internal/input"
	"sdr35/internal/osm"
	"sdr35/internal/pskreporter"
	"sdr35/internal/radio"
	"sdr35/internal/sysinfo"
	"sdr35/internal/ui"
	"sdr35/internal/web"
)

const defaultHost = "e25wop.thddns.net:2255"

// buildStamp is the build version as YYYYMMDDHHMM (injected by
// build-rg35xx.sh via -ldflags). "0" means a dev build that always
// accepts whatever the update server offers.
var buildStamp = "0"

// buildTime is the human build timestamp (YYYY-MM-DD_HH:MM, also from
// -ldflags; the underscore keeps the value shell-friendly).
var buildTime = "-"

// --- over-the-air updater -------------------------------------------------

const defaultUpdateBase = "https://downloads.catgg.net/sdrg35xx"

type updater struct {
	mu    sync.Mutex
	msg   string
	msgAt time.Time
	busy  bool
}

func (u *updater) setMsg(format string, a ...any) {
	u.mu.Lock()
	u.msg = fmt.Sprintf(format, a...)
	u.msgAt = time.Now()
	u.mu.Unlock()
}

// Msg returns the last updater message only while it is still fresh.
// Status lines are transient: without the expiry, the boot-time
// auto-check's "อัพเดทล่าสุดแล้ว" sits in u.msg forever and masks every
// later status (the Y-button FT8 sync confirmation never shows). While
// busy (download/verify/install) the message stays up for the whole
// operation, since the process re-execs right after a successful one.
func (u *updater) Msg() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.msg == "" {
		return ""
	}
	if !u.busy && time.Since(u.msgAt) > 8*time.Second {
		return ""
	}
	return u.msg
}

func (u *updater) tryBegin() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy {
		return false
	}
	u.busy = true
	return true
}

func (u *updater) end() {
	u.mu.Lock()
	u.busy = false
	u.mu.Unlock()
}

// updateHTTPClient returns the client used for OTA requests. If strict
// TLS fails because the console clock is wrong (cert validity window),
// retry once with verification relaxed — payload integrity still rests
// on the sha256 in version.txt.
func updateGet(url string, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err == nil {
		return resp, nil
	}
	if !strings.Contains(err.Error(), "certificate") {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "update: TLS verify failed (%v) — retrying relaxed\n", err)
	client2 := &http.Client{Timeout: timeout, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	return client2.Get(url)
}

func fetchUpdateMeta(base string) (stamp int64, sha string, size int64, err error) {
	resp, err := updateGet(base+"/version.txt", 8*time.Second)
	if err != nil {
		return 0, "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, "", 0, fmt.Errorf("version.txt: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return 0, "", 0, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "stamp":
			stamp, _ = strconv.ParseInt(v, 10, 64)
		case "sha256":
			sha = v
		case "size":
			size, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if stamp == 0 || sha == "" || size == 0 {
		return 0, "", 0, fmt.Errorf("version.txt incomplete")
	}
	return stamp, sha, size, nil
}

// runUpdate checks the server and, when a newer build exists, downloads,
// verifies and swaps the binary, then re-execs into the new version.
func runUpdate(u *updater, base string, manual bool, beforeRestart func()) {
	if !u.tryBegin() {
		return
	}
	go func() {
		defer u.end()
		if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
			if manual {
				u.setMsg("%s", i18n.T("upd_only"))
			}
			return
		}
		local, _ := strconv.ParseInt(buildStamp, 10, 64)
		stamp, sha, size, err := fetchUpdateMeta(base)
		if err != nil {
			if manual {
				u.setMsg("%s", fmt.Sprintf(i18n.T("upd_check_fail"), err))
			}
			return
		}
		if local > 0 && stamp <= local {
			u.setMsg("%s", fmt.Sprintf(i18n.T("uptodate"), buildStamp))
			return
		}
		u.setMsg("%s", fmt.Sprintf(i18n.T("downloading"), stamp))

		resp, err := updateGet(fmt.Sprintf("%s/sdrg35xx-linux-arm64.gz", base), 180*time.Second)
		if err != nil {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_dl_fail"), err))
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_http"), resp.StatusCode))
			return
		}
		gz, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_dl_fail"), err))
			return
		}
		// version.txt carries the sha256 of the GZIPPED package (what
		// upload.sh hashes) — verify both before touching the disk.
		if size > 0 && int64(len(gz)) != size {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_size"), len(gz), size))
			return
		}
		sum := sha256.Sum256(gz)
		if fmt.Sprintf("%x", sum) != sha {
			u.setMsg("%s", i18n.T("checksum"))
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_gzip"), err))
			return
		}
		bin, err := io.ReadAll(zr)
		if err != nil {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_gzip"), err))
			return
		}

		exe, err := os.Executable()
		if err != nil {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_nopath"), err))
			return
		}
		dir := filepath.Dir(exe)
		tmp := filepath.Join(dir, ".sdrg35xx.download")
		if err := os.WriteFile(tmp, bin, 0o755); err != nil {
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_write"), err))
			return
		}
		// Same-directory rename: atomic on the SD card's filesystem; the
		// running old inode stays alive until exit.
		if err := os.Rename(tmp, exe); err != nil {
			os.Remove(tmp)
			u.setMsg("%s", fmt.Sprintf(i18n.T("upd_swap"), err))
			return
		}
		// One-time sidecars for the USB sources: users who installed via
		// OTA have no SD-card copy of rtl_tcp / gpsread. Fetch them
		// opportunistically (integrity = size sanity only; the exe above
		// is hash-pinned) so USB radio + GPS work right after this
		// update. Failures are non-fatal.
		for _, side := range []string{"rtl_tcp", "gpsread", "hamnoise"} {
			fetchSidecar(base, filepath.Dir(exe), side)
		}
		u.setMsg("%s", fmt.Sprintf(i18n.T("updated"), stamp))
		fmt.Fprintf(os.Stderr, "update: installed stamp %d (was %s), re-exec\n", stamp, buildStamp)
		time.Sleep(700 * time.Millisecond) // let the message reach the screen
		syncDir(dir)
		if beforeRestart != nil {
			// Persist this session before the process is replaced —
			// Exec skips the quit path, so settings would revert to
			// the last saved ini without this.
			beforeRestart()
		}
		syscall.Exec(exe, os.Args, os.Environ())
		u.setMsg("%s", i18n.T("upd_restart"))
	}()
}

// fetchSidecar downloads a bundled helper binary (rtl_tcp for the USB
// dongle source, gpsread for the USB GPS) next to the app binary if it
// is not there yet (called from the OTA path — the only moment every
// user is guaranteed to be online). Best effort: on any failure the
// feature just reports "executable not found" and the README's manual
// copy still works. minBytes tolerates the small gpsread helper.
func fetchSidecar(base, dir, name string) {
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err == nil {
		return // already installed (SD-card copy or previous fetch)
	}
	resp, err := updateGet(fmt.Sprintf("%s/%s-linux-arm64.gz", base, name), 60*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sidecar %s: fetch failed: %v\n", name, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "sidecar %s: HTTP %d\n", name, resp.StatusCode)
		return
	}
	gz, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || len(gz) < 3_000 {
		fmt.Fprintf(os.Stderr, "sidecar %s: bad download (%v, %d bytes)\n", name, err, len(gz))
		return
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sidecar %s: gzip: %v\n", name, err)
		return
	}
	bin, err := io.ReadAll(io.LimitReader(zr, 4<<20))
	if err != nil || len(bin) < 8_000 {
		fmt.Fprintf(os.Stderr, "sidecar %s: bad payload (%v, %d bytes)\n", name, err, len(bin))
		return
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "sidecar %s: write: %v\n", name, err)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		fmt.Fprintf(os.Stderr, "sidecar %s: rename: %v\n", name, err)
		return
	}
	fmt.Fprintf(os.Stderr, "sidecar: installed %s (%d bytes)\n", name, len(bin))
}

// fetchDeepCWBundle downloads the DeepCW sidecar bundle (sidecar
// binary + ONNX Runtime libs + model, ~38 MB gz) next to the app binary
// on first enable. The model is far too large for the OTA package, so
// it is fetched lazily — exactly once, and only for users who turn the
// neural CW decoder on. Best effort: any failure leaves the feature
// reporting "not installed".
func fetchDeepCWBundle(base, dir string) {
	dst := filepath.Join(dir, "deepcw")
	if deepcw.Available(dir) {
		return
	}
	resp, err := updateGet(base+"/deepcw-bundle-linux-arm64.tar.gz", 300*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "deepcw bundle: fetch failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "deepcw bundle: HTTP %d\n", resp.StatusCode)
		return
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "deepcw bundle: gzip: %v\n", err)
		return
	}
	tr := tar.NewReader(zr)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "deepcw bundle: tar: %v\n", err)
			return
		}
		name := filepath.Base(h.Name) // never honour paths from the archive
		out := filepath.Join(dir, name)
		f, err := os.OpenFile(out+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			fmt.Fprintf(os.Stderr, "deepcw bundle: open %s: %v\n", name, err)
			return
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 64<<20)); err != nil {
			f.Close()
			os.Remove(out + ".tmp")
			fmt.Fprintf(os.Stderr, "deepcw bundle: copy %s: %v\n", name, err)
			return
		}
		f.Close()
		if err := os.Rename(out+".tmp", out); err != nil {
			fmt.Fprintf(os.Stderr, "deepcw bundle: rename %s: %v\n", name, err)
			return
		}
		n++
	}
	fmt.Fprintf(os.Stderr, "deepcw bundle: installed %d files\n", n)
	_ = dst
}

// fetchFT8TSBundle downloads the ft8ts sidecar bundle (node runtime +
// ft8ts library + sidecar script, ~30 MB gz) next to the app binary on
// first enable. GPL-3.0 code stays in the sidecar process; the bundle
// is far too large for the OTA package, so it is fetched lazily.
func fetchFT8TSBundle(base, dir string) {
	if ft8ts.Available(dir) {
		return
	}
	resp, err := updateGet(base+"/ft8ts-bundle-linux-arm64.tar.gz", 300*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ft8ts bundle: fetch failed: %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "ft8ts bundle: HTTP %d\n", resp.StatusCode)
		return
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, 200<<20))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ft8ts bundle: gzip: %v\n", err)
		return
	}
	tr := tar.NewReader(zr)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "ft8ts bundle: tar: %v\n", err)
			return
		}
		name := filepath.Base(h.Name)
		out := filepath.Join(dir, name)
		f, err := os.OpenFile(out+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ft8ts bundle: open %s: %v\n", name, err)
			return
		}
		if _, err := io.Copy(f, io.LimitReader(tr, 128<<20)); err != nil {
			f.Close()
			os.Remove(out + ".tmp")
			fmt.Fprintf(os.Stderr, "ft8ts bundle: copy %s: %v\n", name, err)
			return
		}
		f.Close()
		if err := os.Rename(out+".tmp", out); err != nil {
			fmt.Fprintf(os.Stderr, "ft8ts bundle: rename %s: %v\n", name, err)
			return
		}
		n++
	}
	fmt.Fprintf(os.Stderr, "ft8ts bundle: installed %d files\n", n)
}

// syncDir flushes the SD card buffers as far as the OS allows.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		f.Sync()
		f.Close()
	}
}

// ft8Bands: standard FT8 dial frequencies (sigidwiki FT8 wiki, the
// "All"/WSJT-X default column). Thailand is IARU Region 3, where the
// "All" entries are the convention, so Region-1 alternates are left out
// to keep the picker to one screen.
var ft8Bands = []struct {
	label string
	hz    int64
}{
	{"160m    1.840 MHz", 1_840_000},
	{"80m     3.573 MHz", 3_573_000},
	{"60m     5.357 MHz", 5_357_000},
	{"40m     7.074 MHz", 7_074_000},
	{"30m    10.136 MHz", 10_136_000},
	{"20m    14.074 MHz", 14_074_000},
	{"17m    18.100 MHz", 18_100_000},
	{"15m    21.074 MHz", 21_074_000},
	{"12m    24.915 MHz", 24_915_000},
	{"10m    28.074 MHz", 28_074_000},
	{"6m     50.313 MHz", 50_313_000},
	{"2m    144.174 MHz", 144_174_000},
	{"70cm  432.174 MHz", 432_174_000},
	{"23cm 1296.174 MHz", 1_296_174_000},
}

// Audio corner-filter options (0 = off); ascending so RIGHT increases.
var audioHpSteps = []int{0, 100, 150, 200, 300, 400, 500, 700, 1000}
var audioLpSteps = []int{0, 1500, 1800, 2000, 2200, 2500, 2800, 3000, 3500}

// audioFilterPresets: one-touch HP+LP combos ("audio filter" row).
// Index 0..2; hp/lp outside every preset reads as custom.
var audioFilterPresets = []struct {
	i18nKey    string
	hpHz, lpHz int
}{
	{"af_narrow", 400, 1700},
	{"af_normal", 300, 2400},
	{"af_wide", 100, 3000},
}

// audioFilterPresetIndex returns the preset matching the corners, or
// -1 when the user shaped them by hand (custom).
func audioFilterPresetIndex(hpHz, lpHz int) int {
	for i, p := range audioFilterPresets {
		if p.hpHz == hpHz && p.lpHz == lpHz {
			return i
		}
	}
	return -1
}

// nextAudioFilterPreset picks the preset to apply when the row is
// pressed: from a matching preset move by dir (wrapping); from custom
// land on normal whichever way is pressed.
func nextAudioFilterPreset(hpHz, lpHz, dir int) int {
	cur := audioFilterPresetIndex(hpHz, lpHz)
	if cur < 0 {
		return 1 // normal
	}
	return (cur + dir + len(audioFilterPresets)) % len(audioFilterPresets)
}

// stepHzOption walks options up/down from cur (matching value or the
// nearest lower entry), wrapping around.
func stepHzOption(cur, dir int, options []int) int {
	idx := 0
	for i, v := range options {
		if v <= cur {
			idx = i
		}
	}
	idx = (idx + dir + len(options)) % len(options)
	return options[idx]
}

func hp2(r *radio.Radio) int {
	hp, _ := r.AudioFilter()
	return hp
}

func lp2(r *radio.Radio) int {
	_, lp := r.AudioFilter()
	return lp
}

// Menu row ids: MUST match the items slices built for DrawMenu.
const menuFreq = 0
const (
	menuMode = iota + 1
	menuGain
	menuSQL
	menuSample
	menuBW
	menuDS
	menuFT8
	menuAGC
	menuHost
	menuLang
	menuSpan
	menuStep
	menuVolume
	menuShot
	menuUpdate
	menuWeb
	menuWebPort
	menuLocalMute
	menuCall
	menuGrid
	menuPSK
	menuSysMon
	menuAnt
	menuRig
	menuWFMin
	menuWFMax
	menuLogs
	menuBM
	menuMap
	menuBands
	menuNR
	menuHP
	menuLP
	menuAF
	menuNRNN
	menuRTTY
	menuRTTYLog
	menuWefax
	menuWefaxClear
	menuWefaxAuto
	menuCWDec
	menuCWClear
	menuDeepCW
	menuDeepCWThreads
	menuDeepCWWindow
	menuDeepCWClear
	menuFT8TS
	menuFT8TSDepth
	menuFT8TSThreads
	menuFT8TSBand
	menuSSTV
	menuSSTVView
	menuSSTVClear
	menuADSBHost
	menuADSBRF
	menuRTLSrv
	menuRTLSrvPort
	menuADSBLat
	menuADSBLon
	menuADSBRadar
	menuAISServer
	menuAISRF
	menuAISLog
	menuClearMap
	menuPPM
	menuGPSDev
	menuGPSStat
	menuGPSTime
	menuGPSPos
	menuGPSGrid
	menuGPSAlt
	menuGPSSpd
	menuGPSCourse
	menuGPSSats
	menuGPSHdop
	menuGPSAge
	menuGPSFollow
	menuGPSTimeSync
	menuAPRSRx
	menuAPRSFreq
	menuAPRSCall
	menuAPRSBeacon
	menuAPRSIS
	menuAPRSServer
	menuAPRSPath
	menuAPRSSym
	menuAPRSCmt
	menuAPRSPre
	menuAPRSLvl
	menuAPRSStat
	menuAPRSLog
	menuAPRSIgate
	menuAPRSGateLim
	menuAPRSSrc
	menuAPRSFixLat
	menuAPRSFixLon
	menuAPRSNow
	menuExit
)

// pageItems is package-level so a test can pin it: one row list per
// page, indexed by the page ids above. A silent edit once left the
// root page at four rows while a fifth page existed — the Audio row
// was unreachable from the d-pad.
var pageItems = [][]int{
	{0, 0, 0, 0, 0, 0, 0, 0, menuBM, 0, menuExit}, // rows open subpages by position (bookmarks row handled first); last row is Exit
	{menuHost, menuSample, menuFreq, menuStep, menuPPM, menuMode, menuGain, menuSQL, menuBW, menuDS, menuAGC},
	{menuAF, menuNR, menuHP, menuLP, menuNRNN, menuLocalMute, menuVolume},
	{menuSpan, menuWFMin, menuWFMax},
	{menuADSBRadar, menuADSBLat, menuADSBLon, menuADSBRF, menuRTLSrv, menuRTLSrvPort, menuADSBHost, menuAISServer, menuAISRF, menuAISLog, menuClearMap},
	{menuGPSDev, menuGPSStat, menuGPSTime, menuGPSPos, menuGPSGrid, menuGPSAlt, menuGPSSpd, menuGPSCourse, menuGPSSats, menuGPSHdop, menuGPSAge, menuGPSFollow, menuGPSTimeSync},
	{menuAPRSRx, menuAPRSFreq, menuAPRSCall, menuAPRSBeacon, menuAPRSIS, menuAPRSServer, menuAPRSPath, menuAPRSSym, menuAPRSCmt, menuAPRSPre, menuAPRSLvl, menuAPRSStat, menuAPRSLog, menuAPRSIgate, menuAPRSGateLim, menuAPRSSrc, menuAPRSFixLat, menuAPRSFixLon, menuAPRSNow},
	{menuMap, menuFT8, menuBands, menuRTTY, menuRTTYLog, menuWefax, menuWefaxAuto, menuWefaxClear, menuCWDec, menuCWClear, menuDeepCW, menuDeepCWThreads, menuDeepCWWindow, menuDeepCWClear, menuFT8TS, menuFT8TSDepth, menuFT8TSThreads, menuFT8TSBand, menuSSTV, menuSSTVView, menuSSTVClear},
	{menuCall, menuGrid, menuAnt, menuRig, menuPSK},
	{menuWeb, menuWebPort, menuLang, menuSysMon, menuLogs, menuShot, menuUpdate},
}

// The flat 16-row menu outgrew the screen, so it is now subpages
// reached from a root list. pageItems maps (page → row) to the item
// ids that adjustItem/activateItem already dispatch on. Root rows open
// subpages BY POSITION (row i opens page i+1), so the page const order
// MUST mirror the root row order: System last, Bookmarks before it.
const (
	pageRoot = iota
	pageRx
	pageAudio
	pageDisp
	pageADSB
	pageGPS
	pageAPRS
	pageFT8
	pageStation
	pageSys
)

// beaconFix resolves the position a beacon reports: the live GPS fix
// (fresh within 10 s) or, when the source is set to FIX, the manually
// entered coordinates as a synthetic fix.
func beaconFix(src string, gpsRx *gps.Receiver, lat, lon float64) (gps.Fix, bool) {
	if src == "fix" {
		if lat == 0 && lon == 0 {
			return gps.Fix{}, false
		}
		return gps.Fix{Lat: lat, Lon: lon, Valid: true, Quality: 1, Updated: time.Now()}, true
	}
	f := gpsRx.Snapshot()
	if !f.Valid || time.Since(f.Updated) > 10*time.Second {
		return gps.Fix{}, false
	}
	return f, true
}

// sendAPRSNow fires a manual/test beacon immediately (menu row A).
func sendAPRSNow(r *radio.Radio, gpsRx *gps.Receiver, call, path string, sym struct {
	i18nKey string
	table   byte
	sym     byte
}, comment string, lvl int, pre float64, isOn bool, iserver string, src string, fixLat, fixLon float64, store *aprs.Store, lastBeacon *time.Time, lastCourse *float64, setMsg func(string)) {
	if call == "" {
		setMsg(i18n.T("aprs_nocall"))
		return
	}
	f, have := beaconFix(src, gpsRx, fixLat, fixLon)
	if !have {
		setMsg(i18n.T("aprs_nogps"))
		return
	}
	if body, info, ok := buildAPRSBeacon(f, call, path, sym, comment); ok {
		*lastBeacon = time.Now()
		*lastCourse = f.CourseDeg
		r.PlayBeacon(aprs.Modulate(body, float64(lvl)/100*0.9, aprsPreambleFlags(pre)))
		setMsg(i18n.T("aprs_sent"))
		store.SetOwn(aprs.Station{Call: call, Lat: f.Lat, Lon: f.Lon,
			Table: sym.table, Sym: sym.sym,
			SpeedKt: f.SpeedKt, CourseDeg: f.CourseDeg, HasCS: f.SpeedKt >= 1,
			AltFt: int(f.Alt * 3.28084), HasAlt: f.Alt != 0, Comment: comment})
		store.LogTX(aprs.LogEntry{At: time.Now(), Call: call, Lat: f.Lat, Lon: f.Lon,
			SpeedKt: f.SpeedKt, AltFt: int(f.Alt * 3.28084), Comment: comment, Info: info, Via: "RF", Sym: string(sym.sym)})
		if isOn {
			store.LogTX(aprs.LogEntry{At: time.Now(), Call: call, Lat: f.Lat, Lon: f.Lon,
				SpeedKt: f.SpeedKt, AltFt: int(f.Alt * 3.28084), Comment: comment, Info: info, Via: "IS", Sym: string(sym.sym)})
		}
		postAPRSIS(isOn, iserver, call, path, info)
	}
}

// postAPRSIS mirrors the beacon onto the APRS-IS network. Runs on its
// own goroutine — the UI loop must never wait on the internet.
func postAPRSIS(on bool, server, call, path, info string) {
	if !on || call == "" {
		return
	}
	go func() {
		if err := aprs.PostIS(server, call, "APRS", path, info); err != nil {
			fmt.Fprintf(os.Stderr, "aprs-is: %v"+string(rune(10)), err)
		} else {
			fmt.Fprintf(os.Stderr, "aprs-is: posted"+string(rune(10)))
		}
	}()
}

// aprsPreambleFlags converts the VOX preamble seconds to flag count
// (8 bits per flag at 1200 baud).
func aprsPreambleFlags(seconds float64) int {
	n := int(math.Round(seconds * 150))
	if n < 1 {
		n = 1
	}
	return n
}

// gridCache remembers each FT8 station's maidenhead grid from their
// CQ/contact messages; shared by the map screen and the web grid
// provider.
var gridCache map[string]string

// Root-row dispatch helpers: the root list mixes positional subpage
// rows with two special rows (Bookmarks between Station and System,
// Exit last). Keeping the mapping in one place lets menu_test pin it
// — an off-by-one here once opened System when Bookmarks was pressed
// and indexed page 10 (crash) when System was pressed.
type rootAction int

const (
	rootPage rootAction = iota
	rootBookmarks
	rootExit
)

func rootRowAction(sel, idx int) rootAction {
	if idx == menuExit {
		return rootExit
	}
	if idx == menuBM {
		return rootBookmarks
	}
	_ = sel
	return rootPage
}

func rootRowPage(sel int) int {
	if sel < 8 {
		return sel + 1
	}
	return pageSys
}

// buildAPRSBeacon renders the UI frame body for a position beacon from
// a live GPS fix (uncompressed format: table+symbol, course/speed when
// moving, altitude).
func buildAPRSBeacon(f gps.Fix, call, path string, sym struct {
	i18nKey string
	table   byte
	sym     byte
}, comment string) (body []byte, info string, ok bool) {
	if !f.Valid || call == "" {
		return nil, "", false
	}
	pos := aprs.Position{Table: sym.table, Sym: sym.sym}
	pos.Lat, pos.Lon = f.Lat, f.Lon
	if f.SpeedKt >= 1 {
		pos.CourseDeg, pos.SpeedKt, pos.HasCS = f.CourseDeg, f.SpeedKt, true
	}
	if f.Alt != 0 {
		pos.AltFt, pos.HasAlt = int(math.Round(f.Alt*3.28084)), true
	}
	info = "!" + aprs.FormatPosition(pos, comment)
	var digis []string
	for _, d := range strings.Split(path, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			digis = append(digis, d)
		}
	}
	return aprs.EncodeUI(call, "APRS", digis, []byte(info)), info, true
}

func main() {
	if !lockInstance() {
		return
	}
	host := flag.String("host", defaultHost, "rtl_tcp server address host:port")
	freq := flag.Int64("freq", 145_500_000, "startup frequency in Hz")
	mode := flag.String("mode", "nfm", "demodulator: nfm | wfm")
	gain := flag.Float64("gain", 40.0, "tuner gain in dB at connect (-1 = AGC)")
	vol := flag.Float64("vol", 0.5, "software volume 0..1.5")
	display := flag.String("display", "auto", `display: "auto" (/dev/fb0) or "png:file.png"`)
	screenshot := flag.Float64("screenshot", 0, "run N seconds then exit (for PNG display testing)")
	demo := flag.Bool("demo", false, "synthetic signal source (no network/dongle)")
	flag.Parse()

	dspMode := dsp.ModeNFM
	if strings.EqualFold(*mode, "wfm") {
		dspMode = dsp.ModeWFM
	}

	// Config file next to the binary remembers the last session.
	cfg := loadConfig()
	if v, ok := cfg["host"]; ok && *host == defaultHost {
		if v == "off" {
			*host = "" // radio disabled — viewer-only session
		} else if v != "" {
			*host = v
		}
	}
	if v, ok := cfg["freq"]; ok && *freq == 145_500_000 {
		if f, err := strconv.ParseInt(v, 10, 64); err == nil {
			*freq = f
		}
	}
	if v, ok := cfg["mode"]; ok && *mode == "nfm" {
		for _, m := range dsp.ModeList {
			if strings.EqualFold(v, m.Name) {
				dspMode = m
				break
			}
		}
	}
	if v, ok := cfg["gain"]; ok && *gain == 40.0 {
		if g, err := strconv.ParseFloat(v, 64); err == nil {
			*gain = g
		}
	}
	rate := 2_048_000
	if v, ok := cfg["rate"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			for _, r := range []int{256_000, 1_024_000, 1_536_000, 1_792_000, 2_048_000, 2_560_000, 2_880_000, 3_200_000} {
				if n == r {
					rate = n
					break
				}
			}
		}
	}
	nrLevel, hpHz, lpHz := 0, 0, 0
	if v, ok := cfg["nr"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 9 {
			nrLevel = n
		}
	}
	if v, ok := cfg["hp"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6000 {
			hpHz = n
		}
	}
	if v, ok := cfg["lp"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6000 {
			lpHz = n
		}
	}
	if v, ok := cfg["lang"]; ok {
		i18n.SetLang(v)
	}
	myCall := strings.ToUpper(strings.TrimSpace(cfg["call"]))
	myGrid := strings.ToUpper(strings.TrimSpace(cfg["grid"]))
	myAnt := strings.TrimSpace(cfg["antenna"])
	myRig := strings.TrimSpace(cfg["rig"])
	pskOn := cfg["psk"] == "on"
	rttyOn := cfg["rtty"] == "on"
	wefaxOn := cfg["wefax"] == "on"
	wefaxAuto := cfg["wefaxauto"] == "on"
	cwDec := cfg["cwdec"] == "on"
	sqlPref := 0.0
	if v, ok := cfg["sql"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= -100 && f <= 40 {
			sqlPref = f
		}
	}
	agcOn := true
	if v, ok := cfg["agc"]; ok && v == "off" {
		agcOn = false
	}
	ds := -1
	if v, ok := cfg["ds"]; ok {
		switch v {
		case "on":
			ds = 2
		case "off":
			ds = 0
		}
	}
	span := 0
	if v, ok := cfg["span"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 3 && n <= 2048 {
			span = n
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Audio first so the speaker stays owned by one process for the whole
	// session; a missing backend is not fatal (waterfall still runs).
	out, audioErr := audio.Start()
	if audioErr != nil {
		fmt.Fprintf(os.Stderr, "audio disabled: %v\n", audioErr)
	}

	var r *radio.Radio
	if *demo {
		r = radio.NewDemo(dspMode, out)
	} else {
		r = radio.New(*host, *freq, dspMode, *gain, out)
	}
	// Every exit path (menu Exit, SIGTERM, ctx cancel) must release the
	// USB dongle: the radio's Run goroutine is not waited for on quit, so
	// its own defer can race the process exit.
	defer func() { r.StopUSB() }()
	if rate != 2_048_000 {
		r.SetCaptureRate(rate)
	}
	if v, ok := cfg["vol"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1.5 {
			*vol = f
		}
	}
	r.SetVolume(*vol)
	// ADS-B: Beast client settings + radar range (L1/R1 cycles).
	// Restore the saved ADS-B/AIS endpoints. These three were written to
	// the ini but never read back, so a hand-edited Beast server /
	// receiver position silently reverted to the defaults on restart.
	adsbHost := "192.168.2.152:30005"
	if v, ok := cfg["adsbhost"]; ok && v != "" && v != "off" {
		adsbHost = v
	} else if v == "off" {
		adsbHost = ""
	}
	adsbLat, adsbLon := 13.5955, 100.56178
	if v, ok := cfg["adsblat"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			adsbLat = f
		}
	}
	if v, ok := cfg["adsblon"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			adsbLon = f
		}
	}
	adsbRanges := []float64{0.5, 1, 2.5, 5, 10, 25, 50, 100, 200, 400}
	adsbRangeIdx := 3
	if v := os.Getenv("SDR_ADSB_RANGE"); v != "" {
		for i, rg := range adsbRanges {
			if fmt.Sprintf("%.0f", rg) == v {
				adsbRangeIdx = i
			}
		}
	}
	adsbConnected := false
	// DeepCW neural Morse decoder window choices (seconds).
	deepcwWindows := []int{3, 5, 8, 12}
	// ft8ts audio-band presets (low-high Hz).
	ft8tsBands := [][2]int{{200, 3000}, {200, 1000}, {1000, 3000}, {500, 2500}}
	// rtl_tcp fan-out server: share the live IQ with other hosts.
	rtlSrvPort := 1235
	if v, ok := cfg["rtlsrvport"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 65535 {
			rtlSrvPort = n
		}
	}
	// OSM basemap: one mosaic per (layer, range zoom), fetched in the
	// background and cached in memory + on the SD card.
	adsbLayerIdx := 0
	if v, ok := cfg["adsblayer"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(osm.Layers) {
			adsbLayerIdx = n
		}
	}
	if v := os.Getenv("SDR_ADSB_LAYER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(osm.Layers) {
			adsbLayerIdx = n
		}
	}
	// Zooms sized so the labeled range fits the screen at the map's native
	// scale (maxR px = rangeKm × pxPerKm of that zoom) — the old
	// 12/11/10/9/8 map was ~1.7× more zoomed-in than the rings, so
	// every blip sat ~1.7× too far from the receiver.
	// One zoom level per range step keeps the labelled distance the same
	// apparent size on screen (≈135-170 px) at every zoom.
	adsbZooms := []int{15, 14, 13, 12, 11, 10, 9, 8, 7, 6} // for ranges 0.5/1/2.5/5/10/25/50/100/200/400 km
	osmCache := osm.NewCache(filepath.Join(filepath.Dir(mustExe()), "osmcache"))
	adsbMosaic := make([][]*image.RGBA, len(osm.Layers))
	adsbFetching := make([][]bool, len(osm.Layers))
	// World-px centre each mosaic was stitched at — panning refetches
	// once the view drifts more than ~150 px from it.
	adsbMosaicCtr := make([][][2]float64, len(osm.Layers))
	mapCacheClearedAt := time.Time{} // row shows "cleared" for a beat after
	// D-pad map panning on the radar screen (screen px; 0,0 = receiver
	// centred). Reset on entry, layer or range change; START recentres.
	panX, panY := 0, 0
	radarSel := &ui.RadarSel{} // target selector, anchored by ID
	// Radar view toggles: label visibility (A) and which targets show (X).
	radarLabelMode := 0
	if v, ok := cfg["radarlabel"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 2 {
			radarLabelMode = n
		}
	}
	radarTargetsMask := 7 // bitmask: 1 planes, 2 ships, 4 APRS (7 = all)
	if v, ok := cfg["radartargets"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			switch n {
			case 0:
				radarTargetsMask = 3 // legacy "both"
			case 1:
				radarTargetsMask = 1
			case 2:
				radarTargetsMask = 2
			default:
				if n >= 1 && n <= 7 {
					radarTargetsMask = n
				}
			}
		}
	}
	for i := range adsbMosaic {
		adsbMosaic[i] = make([]*image.RGBA, len(adsbRanges))
		adsbFetching[i] = make([]bool, len(adsbRanges))
		adsbMosaicCtr[i] = make([][2]float64, len(adsbRanges))
	}
	// The 1024×768 stitched mosaics are far too big to keep for every
	// layer×range combination (~120 MB) — switching frees all but the
	// one in flight (tiles stay disk-cached, so a return visit is fast).
	adsbFreeMosaics := func() {
		for i := range adsbMosaic {
			for j := range adsbMosaic[i] {
				adsbMosaic[i][j] = nil
			}
		}
	}
	// AIS: NMEA ship feed from aiscatcher ("off" = feed disabled, RF
	// decode only).
	aisHost := "192.168.2.151:29420"
	if v, ok := cfg["aishost"]; ok && v != "" && v != "off" {
		aisHost = v
	} else if v == "off" {
		aisHost = ""
	}
	aisConnected := false
	aisShowName := cfg["aisname"] != "false"
	aisLog := []ui.AISEntry{}
	aisScroll := 0
	aisLogMu := sync.Mutex{}
	fmt.Fprintf(os.Stderr, "adsb: beast=%s pos=%.5f,%.5f layer=%d | ais: %s", adsbHost, adsbLat, adsbLon, adsbLayerIdx, aisHost)

	// ADS-B store + Beast feed: created before the radio streams so the
	// in-app RF demodulator can share the same store.
	adsbStore := adsb.NewStore()
	r.ADSBStore(adsbStore)
	go r.Run(ctx)
	adsbRegs := adsb.NewRegDB(filepath.Join(filepath.Dir(mustExe()), "adsbreg.txt"))
	flagDir := filepath.Join(filepath.Dir(mustExe()), "flags")
	adsbClient := adsb.NewClient(adsbHost)
	adsbClient.Connected = func(c bool) { adsbConnected = c }
	go adsbClient.Run(ctx, func(msg []byte, mlat uint64, sig int) {
		adsbStore.Decode(msg)
	})
	// Persist the current ADS-B/AIS values even when untouched this
	// session — the ini then always carries the live configuration.
	if cfg["adsbhost"] == "" {
		cfg["adsbhost"] = adsbHost
	}
	if cfg["adsblat"] == "" {
		cfg["adsblat"] = fmt.Sprintf("%.5f", adsbLat)
	}
	if cfg["adsblon"] == "" {
		cfg["adsblon"] = fmt.Sprintf("%.5f", adsbLon)
	}
	if cfg["adsblayer"] == "" {
		cfg["adsblayer"] = fmt.Sprintf("%d", adsbLayerIdx)
	}
	if cfg["aishost"] == "" {
		cfg["aishost"] = aisHost
	}
	aisStore := ais.NewStore()
	// APRS: RF decode onto the radar + AFSK position beacons from the
	// GPS fix, played out of the speaker for a VOX-keyed radio.
	aprsStore := aprs.NewStore()
	if os.Getenv("SDR_ADSB_DEMO") != "" {
		for _, d := range []struct {
			call, info string
			lat, lon   float64
		}{
			{"HS0ABC-9", "!1337.20N/10035.40E>032/028 demo car /A=000040", 13.62, 100.59},
			{"E23AQ", "=1333.00N/10030.00E- home qth", 13.55, 100.50},
		} {
			body := aprs.EncodeUI(d.call, "APRS", []string{"WIDE1-1"}, []byte(d.info))
			lo, hi := aprs.FCSBytes(body)
			aprsStore.ProcessFrame(append(append(body, lo), hi))
		}
		aprsStore.LogTX(aprs.LogEntry{At: time.Now().Add(-2 * time.Minute), Call: "DEMO-1", Lat: 13.60, Lon: 100.55, SpeedKt: 12, Comment: "test beacon", Via: "RF", Sym: ">"})
		aprsStore.SetOwn(aprs.Station{Call: "DEMO-1", Lat: 13.60, Lon: 100.55, Table: '/', Sym: '>', SpeedKt: 12, HasCS: true, Comment: "own position"})
	}
	aprsSymList := []struct {
		i18nKey string
		table   byte
		sym     byte
	}{
		{"sym_person", '/', '['},
		{"sym_car", '/', '>'},
		{"sym_plane", '/', 0x27},
		{"sym_boat", 0x5C, 'Y'},
	}
	aprsSymIdx := 0
	aprsBeaconIdx := 0 // 0 off, 1..5 = minutes, 6 = smart
	aprsLvl := 70      // AFSK amplitude, %
	aprsFreq := int64(144_800_000)
	aprsPath := "WIDE1-1,WIDE2-1"
	aprsCall, aprsCmt := "", ""
	if v, ok := cfg["aprssym"]; ok {
		for i, e := range aprsSymList {
			if fmt.Sprintf("%c%c", e.table, e.sym) == v {
				aprsSymIdx = i
			}
		}
	}
	if v, ok := cfg["aprsbeacon"]; ok {
		for i, m := range []string{"off", "1", "2", "5", "10", "30", "smart"} {
			if v == m {
				aprsBeaconIdx = i
			}
		}
	}
	if v, ok := cfg["aprslvl"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			aprsLvl = n
		}
	}
	if v, ok := cfg["aprsfreq"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0.5 && f < 1766 {
			aprsFreq = int64(f * 1e6)
		}
	}
	if v, ok := cfg["aprspath"]; ok && v != "" {
		aprsPath = v
	}
	if v, ok := cfg["aprscall"]; ok {
		aprsCall = v
	}
	if v, ok := cfg["aprscmt"]; ok {
		aprsCmt = v
	}
	aprsISOn := cfg["aprsis"] == "on"
	aprsIgateOn := cfg["aprsigate"] == "on"
	aprsGateLim := 10 // max gated packets per minute
	if v, ok := cfg["aprsigatelimit"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 60 {
			aprsGateLim = n
		}
	}
	aprsSrc := "gps" // gps | fix
	if v, ok := cfg["aprssrc"]; ok && (v == "gps" || v == "fix") {
		aprsSrc = v
	}
	var aprsFixLat, aprsFixLon float64
	if v, ok := cfg["aprsfixlat"]; ok {
		aprsFixLat, _ = strconv.ParseFloat(v, 64)
	}
	if v, ok := cfg["aprsfixlon"]; ok {
		aprsFixLon, _ = strconv.ParseFloat(v, 64)
	}
	var gateMu sync.Mutex
	gateMinute := time.Time{}
	gateCount := 0
	gateLast := map[string]time.Time{}
	aprsIServer := "rotate.aprs2.net:14580"
	if v, ok := cfg["aprsiserver"]; ok && strings.Contains(v, ":") {
		aprsIServer = v
	}
	aprsPre := 0.3 // VOX open time, seconds (0.1..2)
	if v, ok := cfg["aprspre"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0.1 && f <= 2.0 {
			aprsPre = f
		}
	}
	if aprsCall == "" {
		aprsCall = cfg["call"] // default to the FT8 callsign (SSID optional)
	}
	aprsLastBeacon := time.Now().Add(-time.Hour)
	aprsLastCourse := -1.0
	aprsLogTab := 0
	sstvVer := -1
	var sstvCache *image.NRGBA
	aprsLogScroll := 0
	// LAN web control (opt-in via System menu; ini web=on/webport=N).
	webPort := 8080
	if v, ok := cfg["webport"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1024 && n <= 65535 {
			webPort = n
		}
	}
	webSrv := web.New(r, adsbStore, aisStore, webPort, osmCache, adsbLat, adsbLon)
	if cfg["web"] == "on" {
		webSrv.SetEnabled(true)
	}
	// USB GPS receiver (NMEA over ttyACM/ttyUSB): auto-detected, read
	// continuously; the GPS page shows the live fix and "follow" hands
	// the position to the radar/web receiver location.
	gpsRx := gps.New()
	gpsFollow := cfg["gpsfollow"] == "on"
	gpsTimeSync := true
	gpsLastClockCheck := time.Now().Add(-time.Hour) // allow first sync soon
	if v, ok := cfg["gpstime"]; ok && v == "off" {
		gpsTimeSync = false
	}
	go gpsRx.Run(ctx)
	webSrv.SetRxPosFunc(func() (float64, float64) {
		if gpsFollow {
			if f := gpsRx.Snapshot(); f.Valid && time.Since(f.Updated) < 10*time.Second {
				return f.Lat, f.Lon
			}
		}
		return adsbLat, adsbLon
	})
	webSrv.SetGPSProvider(func() web.GPSInfo {
		f := gpsRx.Snapshot()
		g := web.GPSInfo{
			Device: gpsRx.Device(), Valid: f.Valid,
			Lat: f.Lat, Lon: f.Lon, Alt: f.Alt,
			SpeedKt: f.SpeedKt, Course: f.CourseDeg,
			SatsUsed: f.SatsUsed, SatsView: f.SatsView,
			HDOP: f.HDOP, Follow: gpsFollow,
			TimeUTC: f.TimeUTC, DateUTC: f.DateUTC,
		}
		if !f.Updated.IsZero() {
			g.AgeSec = time.Since(f.Updated).Seconds()
		}
		if f.Lat != 0 || f.Lon != 0 {
			g.Grid = gps.Maidenhead(f.Lat, f.Lon)
		}
		return g
	})
	aisClient := ais.NewClient(aisHost)
	aisClient.Connected = func(c bool) { aisConnected = c }
	go aisClient.Run(ctx, aisStore)
	// Over-the-air AIS: decoded frames land in the same store (radar
	// needs no changes) and in the message log windows.
	r.SetAISPayloadFunc(func(payload []byte, ch int, levelDb float64) {
		typ, mmsi := aisStore.DecodeBits(payload)
		if mmsi == "" || typ == 0 {
			return
		}
		line := fmt.Sprintf("%d", typ)
		if sh := aisStore.Ship(mmsi); sh != nil {
			line = fmt.Sprintf("%s", sh.MMSI)
			if sh.Name != "" {
				line += " " + sh.Name
			}
			if sh.HasPos {
				line += fmt.Sprintf("  %.4f,%.4f", sh.Lat, sh.Lon)
				if sh.SogKt > 0.5 {
					line += fmt.Sprintf("  %.1fkt", sh.SogKt)
				}
			} else {
				line += "  (no pos)"
			}
		}
		name := "A"
		if ch == 1 {
			name = "B"
		}
		cc := geo.MMSICountry(mmsi)
		aisLogMu.Lock()
		aisLog = append(aisLog, ui.AISEntry{Time: time.Now().Format("15:04:05"), Ch: name, Text: line, FlagCC: cc, Db: levelDb})
		webSrv.AddAIS(web.AISLine{Time: time.Now().Format("15:04:05"), Ch: name, Text: line, Db: levelDb})
		if len(aisLog) > 100 {
			aisLog = aisLog[len(aisLog)-100:]
		}
		aisLogMu.Unlock()
	})
	r.SetAPRSFreq(aprsFreq)
	if v := cfg["nrnn"]; v == "voice" || v == "cw" {
		if ex, err := os.Executable(); err == nil {
			r.SetNRDir(filepath.Dir(ex))
		}
		r.SetNREnabled(true, v)
	}
	if cfg["sstv"] == "on" {
		r.SetSSTVEnabled(true)
	}
	if cfg["aprs"] == "on" {
		r.SetAPRSEnabled(true)
	}
	if cfg["aisrf"] == "on" {
		r.SetAISRFEnabled(true)
	}
	if cfg["adsbrf"] == "on" {
		r.SetADSBRFEnabled(true)
	}
	if ex, err := os.Executable(); err == nil {
		r.SetDeepCWDir(filepath.Dir(ex))
	}
	if v, ok := cfg["deepcwth"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			_, w := r.DeepCWParams()
			r.SetDeepCWParams(n, w)
		}
	}
	if v, ok := cfg["deepcwwin"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			t, _ := r.DeepCWParams()
			r.SetDeepCWParams(t, n)
		}
	}
	if cfg["deepcw"] == "on" && r.DeepCWAvailable() {
		r.SetDeepCWEnabled(true)
	}
	if v, ok := cfg["ft8tsdepth"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			_, t, lo, hi := r.FT8TSParams()
			r.SetFT8TSParams(n, t, lo, hi)
		}
	}
	if v, ok := cfg["ft8tsth"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			d, _, lo, hi := r.FT8TSParams()
			r.SetFT8TSParams(d, n, lo, hi)
		}
	}
	if v, ok := cfg["ft8tsband"]; ok {
		var lo, hi int
		if _, err := fmt.Sscanf(v, "%d-%d", &lo, &hi); err == nil {
			d, t, _, _ := r.FT8TSParams()
			r.SetFT8TSParams(d, t, lo, hi)
		}
	}
	if cfg["ft8ts"] == "on" && r.FT8TSAvailable() {
		r.SetFT8TSEnabled(true)
	}
	r.SetRTLSrvPort(rtlSrvPort)
	if cfg["rtlsrv"] == "on" {
		r.SetRTLSrvEnabled(true)
	}
	defer func() {
		if out != nil {
			out.Close()
		}
	}()

	// Over-the-air updater: auto-check after boot (config update=off
	// disables), manual re-check from the settings menu.
	fmt.Fprintf(os.Stderr, "SDRg35xx build %s (%s)\n", buildStamp, strings.ReplaceAll(buildTime, "_", " "))
	upd := &updater{}
	updateBase := defaultUpdateBase
	if v, ok := cfg["updateurl"]; ok && v != "" {
		updateBase = v
	}
	autoUpdate := !strings.EqualFold(cfg["update"], "off")
	if !autoUpdate {
		fmt.Fprintln(os.Stderr, "update: auto-check disabled by config")
	}

	disp, err := ui.OpenDisplay(*display)
	if err != nil {
		fmt.Fprintf(os.Stderr, "display: %v\n", err)
		return
	}
	defer disp.Close()
	dw, dh := disp.Size()
	fmt.Fprintf(os.Stderr, "step: display ok %dx%d\n", dw, dh)
	u := ui.New(dw, dh)
	// Waterfall colour range (menu-adjustable, persisted). Guarded by
	// wfMu: the web server goroutine reads/writes it too.
	wfMu := &sync.Mutex{}
	wfMin, wfMax := 6.0, 62.0
	if v, ok := cfg["wfmin"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 40 {
			wfMin = f
		}
	}
	if v, ok := cfg["wfmax"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 10 && f <= 120 {
			wfMax = f
		}
	}
	// Tuner frequency correction (ppm) — sent live and on every
	// reconnect; "off" leaves the correction to the server itself.
	if v, ok := cfg["ppm"]; ok && v == "off" {
		r.SetPpmOff(true)
	} else if v, ok := cfg["ppm"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			r.SetPpm(n)
		}
	}
	if cfg["ppm"] == "" {
		cfg["ppm"] = "0"
	}
	if cfg["lmute"] == "on" {
		r.SetLocalMute(true)
	}
	u.SetWaterfallRange(wfMin, wfMax)
	if span > 0 {
		u.SetSpanKHz(span)
	}
	if ds != -1 {
		r.SetDirectSamplingMode(ds)
	}
	if sqlPref > 0 {
		r.SetSquelchDb(sqlPref)
	}
	r.SetNoiseReduction(nrLevel)
	r.SetAudioFilter("hp", hpHz)
	r.SetAudioFilter("lp", lpHz)
	r.SetRTTYEnabled(rttyOn)
	r.SetWefaxEnabled(wefaxOn)
	r.SetWefaxAutoSave(wefaxAuto)
	r.SetCWDecodeEnabled(cwDec)
	if bwv, ok := cfg[fmt.Sprintf("bw.%s", r.Mode().Name)]; ok {
		if f, err := strconv.ParseFloat(bwv, 64); err == nil {
			r.SetBandwidth(f)
		}
	}
	if !agcOn {
		r.SetAGCEnabled(false)
	}
	fmt.Fprintf(os.Stderr, "step: ui created\n")

	// Boot frame right away: a solid color on screen proves the whole
	// display path before anything else can hang, and exercises the first
	// Present (which also runs the pan + mirror logic) immediately.
	boot := u.Frame(ui.FrameStats{FreqHz: *freq, LOHz: *freq, Mode: dspMode.Name, StepHz: 12_500, StatusText: i18n.T("starting")})
	if err := disp.Present(boot); err != nil {
		fmt.Fprintf(os.Stderr, "boot present: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "step: boot frame presented\n")

	// Input may be absent on dev machines; the app still runs.
	pad, padErr := input.Open()
	if padErr != nil {
		fmt.Fprintf(os.Stderr, "input disabled: %v\n", padErr)
	} else {
		fmt.Fprintf(os.Stderr, "step: input ok\n")
		defer pad.Close()
	}

	held := map[input.Button]bool{}
	lastRepeat := map[input.Button]time.Time{}
	volHeldAt := map[input.Button]time.Time{}
	// FT8 slot markers: after a Y sync, a red separator is drawn on the
	// waterfall at every 15 s slot boundary so sync accuracy is visible
	// (signals should start right under each line).
	ft8SyncWall := time.Time{}
	ft8SlotIdx := -1
	ft8SlotMark := false
	// Scroll position of the big FT8 history window (entries hidden
	// below the bottom of the view; 0 = newest at the bottom).
	ft8Scroll := 0
	// Tuning step for left/right (up/down is ×10), settable in the menu.
	stepSteps := []int64{10, 50, 100, 500, 1_000, 5_000, 10_000, 12_500, 25_000, 100_000}
	stepHz := int64(12_500)
	if v, ok := cfg["step"]; ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			for _, s := range stepSteps {
				if s == n {
					stepHz = n
				}
			}
		}
	}
	stepFor := func() int64 {
		return stepHz
	}
	stepLabel := func(hz int64) string {
		if hz >= 1000 {
			return fmt.Sprintf("%g kHz", float64(hz)/1000)
		}
		return fmt.Sprintf("%d Hz", hz)
	}
	// autoStep pulls the tune step to sane per-mode defaults after a mode
	// switch — only when the current step is COARSER (a hand-picked finer
	// step survives): WFM channels are 100 kHz apart, USB/LSB want 1 kHz,
	// CW 100 Hz.
	autoStep := func(m dsp.Mode) {
		switch {
		case m == dsp.ModeWFM && stepHz < 25_000:
			stepHz = 100_000
		case m == dsp.ModeCW && stepHz > 100:
			stepHz = 100
		case m == dsp.ModeUSB || m == dsp.ModeLSB:
			if stepHz > 1_000 {
				stepHz = 1_000
			}
		case m != dsp.ModeWFM && stepHz > 25_000:
			stepHz = 12_500
		}
	}
	// Menu Exit: closing this unblocks the render loop's poll below,
	// which runs the same quit path as the hold-to-exit combo.
	exitMenu := make(chan struct{})
	// saveNow flushes the live session state to the ini. Called on
	// quit, BEFORE an update re-exec (which otherwise loses every
	// change made since boot — the bug behind settings reverting
	// after an OTA update), and periodically from the render loop as
	// a power-loss guard.
	var saveMu sync.Mutex
	saveNow := func() {
		saveMu.Lock()
		defer saveMu.Unlock()
		langPref := i18n.Lang()
		agcPref := "on"
		if !r.AGCEnabled() {
			agcPref = "off"
		}
		dsPref := "auto"
		switch r.DirectSamplingMode() {
		case 2:
			dsPref = "on"
		case 0:
			dsPref = "off"
		}
		saveBwNow(cfg, r)
		saveConfig(cfg, *host, r.Freq(), r.Mode().Name, r.Volume(), *gain, r.IQRate(), u.SpanFull/1000, dsPref, agcPref, langPref, stepHz, myCall, myGrid, myAnt, myRig, pskOn, wfMin, wfMax)
	}
	quit := func() {
		saveNow()
		stop()
		// Leaving in dim/off would strand the panel: the firmware has
		// no /sys/class/backlight for the .sh restore loop to use.
		backlight.RestoreOn()
	}
	if autoUpdate {
		runUpdate(upd, updateBase, false, saveNow)
	}
	capturedMsg := ""
	var capturedAt time.Time
	// Web-server goroutines must not touch cfg or the capture message
	// directly — a fatal "concurrent map writes" crashed the app when
	// overlapping slider commands wrote cfg from two HTTP handlers.
	// Web-triggered mutations are posted to webTasks and run on THIS
	// (UI) goroutine, which stays the only cfg writer after boot.
	webTasks := make(chan func(), 128)
	postWeb := func(f func()) bool {
		select {
		case webTasks <- f:
			return true
		default:
			return false
		}
	}
	var msgMu sync.Mutex
	setMsg := func(txt string) {
		msgMu.Lock()
		capturedMsg, capturedAt = txt, time.Now()
		msgMu.Unlock()
	}

	if os.Getenv("SDR_ADSB_DEMO") != "" {
		webSrv.SetExtraTargets(func() []web.Target {
			now := time.Now()
			var out []web.Target
			for _, d := range []struct {
				icao          string
				lat, lon      float64
				alt, spd, trk int
			}{
				{"A1B2C3", 13.72, 100.55, 31000, 460, 75},
				{"D4E5F6", 13.60, 100.70, 9000, 240, 300},
			} {
				t := web.Target{Kind: "plane", ID: d.icao, Call: d.icao, Lat: d.lat, Lon: d.lon,
					HasPos: true, AltFt: d.alt, Speed: float64(d.spd), Track: d.trk, AgeSec: 3}
				// Breadcrumbs backwards along the track, climbing.
				for k := 1; k <= 24; k++ {
					de := math.Sin(float64(d.trk)*math.Pi/180) * 0.02 * float64(k)
					dn := math.Cos(float64(d.trk)*math.Pi/180) * 0.02 * float64(k)
					t.Trail = append(t.Trail, web.TrailPt{Lat: d.lat - dn, Lon: d.lon - de,
						AltFt: d.alt - k*400, AgeSec: float64(k) * 10})
				}
				_ = now
				out = append(out, t)
			}
			return out
		})
	}
	webSrv.SetAPRSProvider(func() []web.APRSStation {
		var out []web.APRSStation
		if os.Getenv("SDR_ADSB_DEMO") != "" {
			now := time.Now()
			out = append(out,
				web.APRSStation{Call: "HS0ABC-9", Lat: 13.62, Lon: 100.59, SpeedKt: 32, Course: 90, Comment: "demo car", AgeSec: now.Sub(now.Add(-4 * time.Minute)).Seconds()},
				web.APRSStation{Call: "E23AQ", Lat: 13.55, Lon: 100.50, Comment: "demo home", AgeSec: 120})
		}
		for _, st := range aprsStore.All() {
			out = append(out, web.APRSStation{Call: st.Call, Lat: st.Lat, Lon: st.Lon,
				SpeedKt: st.SpeedKt, Course: st.CourseDeg, AltFt: st.AltFt,
				Comment: st.Comment, AgeSec: time.Since(st.LastHeard).Seconds(),
				Country: geo.CountryISO(st.Call), Sym: aprs.Emoji(st.Table, st.Sym), SymChar: string(st.Sym), Own: st.Own})
		}
		return out
	})
	if os.Getenv("SDR_MAP_DEMO") != "" || os.Getenv("SDR_ADSB_DEMO") != "" {
		// Dev aid: rolling FT8 traffic so the web map's FT8 layer always
		// has fresh entries inside the 10-minute window (a one-shot seed
		// ages out and the layer goes blank after a few minutes).
		seedFT8 := func() {
			now := time.Now()
			webSrv.ResetFT8()
			for i, m := range []struct {
				dt   time.Duration
				text string
			}{
				{-3 * time.Second, "CQ HS0JR KO85"},
				{-70 * time.Second, "CQ 9M2XYZ OJ02"},
				{-3 * time.Minute, "HS0JR DU1XXX PK04"},
				{-5 * time.Minute, "CQ E21ABC OK03"},
			} {
				webSrv.AddFT8(web.FT8Line{Time: now.Add(m.dt).Format("15:04:05"), SNR: -float64(i), Hz: 500 + float64(i*37), Text: m.text})
			}
		}
		seedFT8()
		go func() {
			for range time.Tick(15 * time.Second) {
				seedFT8()
			}
		}()
	}
	webSrv.SetFT8Grid(func(log []web.FT8Line) []web.FT8MapEntry {
		now := time.Now()
		out := []web.FT8MapEntry{}
		for _, e := range log {
			t, err := time.Parse("15:04:05", e.Time)
			if err != nil {
				continue
			}
			eTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
			if eTime.After(now.Add(time.Hour)) {
				eTime = eTime.Add(-24 * time.Hour)
			}
			age := now.Sub(eTime)
			if age < 0 || age > 10*time.Minute {
				continue
			}
			toks := strings.Fields(e.Text)
			if len(toks) < 2 {
				continue
			}
			isCQ := toks[0] == "CQ" || strings.HasPrefix(toks[0], "CQ_")
			senderGrid := ""
			lastTok := toks[len(toks)-1]
			hasGrid := len(lastTok) == 4 && lastTok[0] >= 'A' && lastTok[0] <= 'R' &&
				lastTok[1] >= 'A' && lastTok[1] <= 'R' &&
				lastTok[2] >= '0' && lastTok[2] <= '9' && lastTok[3] >= '0' && lastTok[3] <= '9'
			if hasGrid {
				senderGrid = lastTok
			} else if g, ok := gridCache[toks[1]]; ok {
				senderGrid = g
			}
			pos := func(call, grid string) (float64, float64, bool, bool) {
				if grid != "" {
					lat, lon, ok := geo.GridToLatLon(grid)
					return lat, lon, false, ok
				}
				lat, lon, ok := geo.CountryLatLon(call)
				return lat, lon, true, ok
			}
			slat, slon, sApprox, sok := pos(toks[1], senderGrid)
			if !sok {
				continue
			}
			ent := web.FT8MapEntry{Lat: slat, Lon: slon, IsCQ: isCQ, Approx: sApprox, AgeSec: age.Seconds(), Call: toks[1], Text: e.Text}
			if !isCQ {
				if rlat, rlon, rApprox, rok := pos(toks[0], gridCache[toks[0]]); rok && (rlat != slat || rlon != slon) {
					ent.Arc = true
					ent.FromLat, ent.FromLon = slat, slon
					ent.Lat, ent.Lon, ent.Approx = rlat, rlon, rApprox
					ent.ToCall = toks[0]
				}
			}
			out = append(out, ent)
		}
		return out
	})
	aprsStore.SetOnFrame(func(f *aprs.Frame) {
		if !aprsIgateOn || aprsCall == "" {
			return
		}
		// Never gate what came off the internet (loop) or our own
		// transmissions (they are posted as first-party beacons).
		if aprs.FrameFromInternet(f) {
			return
		}
		base := f.Src
		if i := strings.IndexByte(base, '-'); i >= 0 {
			base = base[:i]
		}
		own := aprsCall
		if i := strings.IndexByte(own, '-'); i >= 0 {
			own = own[:i]
		}
		if base == own {
			return
		}
		gateMu.Lock()
		now := time.Now()
		if last, ok := gateLast[f.Src]; ok && now.Sub(last) < 30*time.Second {
			gateMu.Unlock()
			return // per-station flood guard
		}
		if now.Sub(gateMinute) >= time.Minute {
			gateMinute, gateCount = now, 0
		}
		if gateCount >= aprsGateLim {
			gateMu.Unlock()
			return // minute budget exhausted
		}
		gateCount++
		gateLast[f.Src] = now
		gateMu.Unlock()
		line := aprs.GateLine(f, aprsCall)
		go func() {
			if err := aprs.PostRawIS(aprsIServer, aprsCall, line); err != nil {
				fmt.Fprintf(os.Stderr, "aprs-gate: %v"+string(rune(10)), err)
			} else {
				aprsStore.LogTX(aprs.LogEntry{At: time.Now(), Call: f.Src, Comment: "gated", Info: line, Via: "GATE"})
			}
		}()
	})
	webSrv.SetAPRSLog(func() web.APRSLog {
		lg := web.APRSLog{}
		for _, st := range aprsStore.All() {
			lg.Stations = append(lg.Stations, web.APRSStation{Call: st.Call, Lat: st.Lat, Lon: st.Lon,
				SpeedKt: st.SpeedKt, Course: st.CourseDeg, AltFt: st.AltFt, Comment: st.Comment,
				AgeSec: time.Since(st.LastHeard).Seconds(), Country: geo.CountryISO(st.Call),
				Sym: aprs.Emoji(st.Table, st.Sym), SymChar: string(st.Sym), Own: st.Own})
		}
		mk := func(es []aprs.LogEntry) []web.APRSLogRow {
			var rows []web.APRSLogRow
			for _, e := range es {
				rows = append(rows, web.APRSLogRow{At: e.At.Unix(), Call: e.Call, Lat: e.Lat, Lon: e.Lon,
					SpeedKt: e.SpeedKt, AltFt: e.AltFt, Comment: e.Comment, Info: e.Info, Via: e.Via})
			}
			return rows
		}
		lg.Rx, lg.Tx = mk(aprsStore.RxLog()), mk(aprsStore.TxLog())
		return lg
	})
	webSrv.SetAPRSState(func() (bool, int, bool, bool, string, int) {
		return r.APRSEnabled(), aprsBeaconIdx, aprsISOn, aprsIgateOn, aprsSrc, aprsStore.Count()
	})
	webSrv.SetAPRSCmd(func(action string, v int) string {
		switch action {
		case "rx":
			on := v != 0
			r.SetAPRSEnabled(on)
			cfg["aprs"] = map[bool]string{true: "on", false: "off"}[on]
			saveNow()
		case "beacon":
			if v >= 0 && v <= 6 {
				aprsBeaconIdx = v
				cfg["aprsbeacon"] = []string{"off", "1", "2", "5", "10", "30", "smart"}[v]
				saveNow()
			}
		case "src":
			if v == 1 {
				aprsSrc = "fix"
			} else {
				aprsSrc = "gps"
			}
			cfg["aprssrc"] = aprsSrc
			saveNow()
		case "igate":
			aprsIgateOn = v != 0
			cfg["aprsigate"] = map[bool]string{true: "on", false: "off"}[aprsIgateOn]
			saveNow()
		case "is":
			aprsISOn = v != 0
			cfg["aprsis"] = map[bool]string{true: "on", false: "off"}[aprsISOn]
			saveNow()
		case "now":
			sendAPRSNow(r, gpsRx, aprsCall, aprsPath, aprsSymList[aprsSymIdx], aprsCmt, aprsLvl, aprsPre, aprsISOn, aprsIServer, aprsSrc, aprsFixLat, aprsFixLon, aprsStore, &aprsLastBeacon, &aprsLastCourse, setMsg)
			return i18n.T("aprs_sent")
		}
		return ""
	})
	// Web-triggered OTA uses the same single-flight updater as the menu.
	webSrv.SetUpdater(upd.Msg, func() { runUpdate(upd, updateBase, true, saveNow) })
	// Web WEFAX save: same file, same folder as the device's Y-save.
	// (Declared early; the implementation is assigned further down
	// next to the other WEFAX save helpers.)
	var saveWefaxFile func(img *image.Gray, auto bool) string
	webSrv.SetWefaxSaver(func() string {
		img := r.Wefax().Snapshot()
		if img == nil {
			return ""
		}
		return saveWefaxFile(img, false)
	})
	// The web WF min/max sliders share the device's waterfall range
	// (same ini keys, persisted immediately like every other mutation).
	webSrv.SetWaterfallRange(
		func() (float64, float64) {
			wfMu.Lock()
			defer wfMu.Unlock()
			return wfMin, wfMax
		},
		func(mn, mx float64) {
			postWeb(func() {
				wfMu.Lock()
				wfMin, wfMax = mn, mx
				wfMu.Unlock()
				u.SetWaterfallRange(mn, mx)
				cfg["wfmin"] = fmt.Sprintf("%g", mn)
				cfg["wfmax"] = fmt.Sprintf("%g", mx)
				saveNow()
			})
		})
	// Screenshot support: the last presented frame and a transient status
	// message pointing at the saved file (triggered from the menu).
	ft8Log := make([]ui.FT8Entry, 0, 100)
	// recentFT8 drives the 6 s duplicate window for decoded messages.
	recentFT8 := make([]ft8Seen, 0, 40)
	// gridCache remembers each station's grid from their CQ/contact
	// messages, so report/RRR/73 messages (which don't carry a grid)
	// can still show the distance.
	gridCache = map[string]string{}
	// PSK Reporter: spots are buffered as they decode and flushed over
	// UDP every 5 minutes (the service asks for at most that rate).
	psk := pskreporter.New(myCall, myGrid, myAnt, myRig, pskOn)
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			psk.Flush()
		}
	}()

	panelState := 0 // power-key cycle: 0=on, 1=dim, 2=off
	// Web panel buttons (screen on/dim/off): applied on the UI goroutine
	// so panelState stays single-writer, same as the power key.
	webSrv.SetPanelStateFunc(func() int { return panelState })
	webSrv.SetPanelFunc(func(state int) {
		postWeb(func() {
			if state >= 0 && state <= 2 {
				panelState = state
				backlight.ApplyState(state)
				fmt.Fprintf(os.Stderr, "panel: web set state %d (%s)\n", state,
					[]string{"on", "dim", "off"}[state])
			}
		})
	})
	// Heal a panel left dim by a previous run that quit in the dim
	// state (its level-10 would otherwise be adopted as "normal").
	backlight.RestoreOn()
	// Log viewer state: the app's own log file, re-read every 2 s.
	logLines := []string{}
	logScroll := 0
	logReadAt := time.Time{}
	var lastFrame *image.RGBA
	// WEFAX saves go to wefax/ next to the binary; auto = APT stop.
	// Returns the written path ("" on error) — the web save uses it.
	saveWefaxFile = func(img *image.Gray, auto bool) string {
		exe, err := os.Executable()
		dir := "."
		if err == nil {
			dir = filepath.Dir(exe)
		}
		dir = filepath.Join(dir, "wefax")
		os.MkdirAll(dir, 0o755)
		name := fmt.Sprintf("wefax_%s_%.4fMHz.png", time.Now().Format("20060102_150405"), float64(r.Freq())/1e6)
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err == nil {
			err = png.Encode(f, img)
			f.Close()
		}
		if err != nil {
			setMsg(i18n.T("shot_fail") + err.Error())
			path = ""
		} else {
			key := "wefax_saved"
			if auto {
				key = "wefax_autosaved"
			}
			setMsg(i18n.T(key) + path)
		}
		fmt.Fprintln(os.Stderr, "wefax:", capturedMsg)
		return path
	}
	saveWefax := func() {
		img := r.Wefax().Snapshot()
		if img == nil {
			setMsg(i18n.T("wefax_empty"))
			return
		}
		saveWefaxFile(img, false)
	}
	capture := func() {
		if lastFrame == nil {
			return
		}
		exe, err := os.Executable()
		dir := "."
		if err == nil {
			dir = filepath.Dir(exe)
		}
		path := filepath.Join(dir, fmt.Sprintf("capture_%s.png", time.Now().Format("150405")))
		if err := ui.SavePNG(path, lastFrame); err != nil {
			setMsg(i18n.T("shot_fail") + err.Error())
		} else {
			setMsg(i18n.T("shot_ok") + path)
		}
		fmt.Fprintln(os.Stderr, capturedMsg)
	}

	// --- UI state machine: main screen / settings menu / freq editor ---
	const (
		uiMain = iota
		uiMenu
		uiFreqEdit
		uiHostEdit
		uiHostList
		uiAISLog
		uiAPRSLog
		uiSSTV
		uiBeastList
		uiAISList
		uiFT8Log
		uiSysMon
		uiLogs
		uiBmList
		uiMap
		uiFT8Bands
		uiRTTY
		uiADSB
	)
	uiMode := uiMain
	// Map screen: selected station (index into mapStationNames, -1 =
	// none) and whether the info panel is open. mapStationNames is
	// rebuilt by the map renderer each frame and read by handlePress.
	mapSel, mapDetail := -1, false
	mapStationNames := []string{}
	// FT8 band picker: selected row (up/down walk the ft8Bands table).
	ft8BandSel := 0
	// RTTY screen: display list + scroll. Lines drain from the decoder
	// every frame while the screen is open.
	rttyLines := []string{}
	rttyScroll := 0
	menuPage := pageRoot
	// Dev aid for PNG screenshot testing of the overlays.
	switch os.Getenv("SDR_UI") {
	case "menu":
		uiMode = uiMenu
	case "freq":
		uiMode = uiFreqEdit
	case "map":
		uiMode = uiMap
	case "ft8bands":
		uiMode = uiFT8Bands
	case "audio":
		uiMode, menuPage = uiMenu, pageAudio
	case "adsbpage":
		uiMode, menuPage = uiMenu, pageADSB
	case "gpspage":
		uiMode, menuPage = uiMenu, pageGPS
	case "rxpage":
		uiMode, menuPage = uiMenu, pageRx
	case "ft8page":
		uiMode, menuPage = uiMenu, pageFT8
	case "stationpage":
		uiMode, menuPage = uiMenu, pageStation
	case "aprspage":
		uiMode, menuPage = uiMenu, pageAPRS
		r.SetAPRSEnabled(true)
	case "aprslog":
		uiMode = uiAPRSLog
	case "syspage":
		uiMode, menuPage = uiMenu, pageSys
	case "rtty":
		uiMode = uiRTTY
	case "adsb":
		uiMode = uiADSB
	}
	menuSel := 0
	// menuRow maps an item id to its row on a page. Hardcoded row
	// numbers drifted every time a row was inserted — this replaces
	// the "pageFT8, N" literals.
	menuRow := func(page, item int) int {
		for i, it := range pageItems[page] {
			if it == item {
				return i
			}
		}
		return 0
	}
	// Span options, finest → widest, so RIGHT widens the span (the value
	// goes UP on right like every other numeric row; the old list ran
	// big→small and right shrank the number). Entries wider than the
	// current capture rate are filtered out per press — SetSpanKHz
	// clamps them anyway, which used to swallow 2-3 presses dead at low
	// rates before the list suddenly wrapped to the finest zoom.
	spanSteps := []int{3, 5, 10, 12, 25, 50, 100, 125, 250, 500, 750, 1000}
	spanStep := func(dir int) {
		maxK := dsp.IQRate / 1000
		allowed := make([]int, 0, len(spanSteps))
		for _, v := range spanSteps {
			if v <= maxK {
				allowed = append(allowed, v)
			}
		}
		if len(allowed) == 0 {
			return
		}
		// Anchor on the LIVE span (clamping may have moved it off any
		// list entry), stepping from the nearest entry at/below it.
		cur := u.SpanFull / 1000
		idx := -1
		for i, v := range allowed {
			if v == cur {
				idx = i
				break
			}
		}
		if idx < 0 {
			for i := len(allowed) - 1; i >= 0; i-- {
				if allowed[i] <= cur {
					idx = i
					break
				}
			}
			if idx < 0 {
				idx = 0
			}
		}
		u.SetSpanKHz(allowed[(idx+len(allowed)+dir)%len(allowed)])
	}

	freqDigits := func() string {
		hz := r.Freq()
		mhz := hz / 1_000_000
		frac := (hz % 1_000_000) / 10 // 5 digits of 10 Hz
		return fmt.Sprintf("%04d%05d", mhz, frac)
	}
	editDigits := freqDigits()
	hostText := *host
	// kbTarget: what the on-screen keyboard is editing ("host"/"call"/"grid").
	kbTarget := "host"
	// Bookmarks: freq|mode|label entries persisted as bm= in the ini.
	type bmT struct {
		freqHz int64
		mode   string
		label  string
	}
	bookmarks := []bmT{}
	if v, ok := cfg["bm"]; ok && v != "" {
		for _, ent := range strings.Split(v, "|") {
			parts := strings.SplitN(ent, ":", 3)
			if len(parts) < 2 {
				continue
			}
			f, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || f < 500_000 {
				continue
			}
			bm := bmT{freqHz: f, mode: parts[1]}
			if len(parts) > 2 {
				bm.label = parts[2]
			}
			bookmarks = append(bookmarks, bm)
		}
	}
	bmSel := 0
	saveBookmarks := func() {
		var parts []string
		for _, bm := range bookmarks {
			parts = append(parts, fmt.Sprintf("%d:%s:%s", bm.freqHz, bm.mode, bm.label))
		}
		cfg["bm"] = strings.Join(parts, "|")
	}
	kbShifted := false
	kbTitle := func() string {
		switch kbTarget {
		case "call":
			return i18n.T("m_call")
		case "aprscall":
			return i18n.T("m_aprscall")
		case "aprspath":
			return i18n.T("m_aprspath")
		case "aprsiserver":
			return i18n.T("m_aprsisrv")
		case "aprsfixlat":
			return i18n.T("m_aprsfixlat")
		case "aprsfixlon":
			return i18n.T("m_aprsfixlon")
		case "aprscmt":
			return i18n.T("m_aprscmt")
		case "aprsfreq":
			return i18n.T("m_aprsfreq")
		case "grid":
			return i18n.T("m_grid")
		case "ant":
			return i18n.T("m_ant")
		case "rig":
			return i18n.T("m_rig")
		case "bm":
			return i18n.T("m_bm")
		case "beast":
			return i18n.T("beast_title")
		case "ais":
			return i18n.T("ais_title")
		}
		return i18n.T("m_host") + ":port"
	}
	hostKbR, hostKbC := 0, 0
	editCursor := 6 // default to the 10 kHz digit (index into 9 digits)
	// Saved host list (hosts= in the ini, comma-separated). The current
	// host is always present so it can be edited or re-selected.
	hostList := []string{}
	if v, ok := cfg["hosts"]; ok && v != "" {
		for _, h := range strings.Split(v, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				hostList = append(hostList, h)
			}
		}
	}
	if len(hostList) == 0 {
		hostList = []string{*host}
	}
	{
		cur := *host
		found := false
		for _, h := range hostList {
			if h == cur {
				found = true
			}
		}
		if !found {
			hostList = append([]string{cur}, hostList...)
		}
	}
	hostSel := 0
	// hostEditIdx: which list entry the keyboard is editing (-1 = new).
	hostEditIdx := -1
	saveHosts := func() {
		cfg["hosts"] = strings.Join(hostList, ",")
	}
	// Beast/AIS server pickers share the host-list mechanics; each list is
	// seeded with the live value so it can be edited or re-selected.
	loadHostList := func(key, cur string) []string {
		var list []string
		if v, ok := cfg[key]; ok && v != "" {
			for _, h := range strings.Split(v, ",") {
				if h = strings.TrimSpace(h); h != "" {
					list = append(list, h)
				}
			}
		}
		found := false
		for _, h := range list {
			if h == cur {
				found = true
			}
		}
		if !found {
			list = append([]string{cur}, list...)
		}
		return list
	}
	beastList := loadHostList("beasthosts", adsbHost)
	aisList := loadHostList("aishosts", aisHost)
	beastSel, aisSel := 0, 0
	saveBeastHosts := func() { cfg["beasthosts"] = strings.Join(beastList, ",") }
	saveAISHosts := func() { cfg["aishosts"] = strings.Join(aisList, ",") }
	listTargetOf := func(mode int) string {
		if mode == uiAISList {
			return "ais"
		}
		return "beast"
	}
	menuBeastRow := func(mode int) int {
		if mode == uiAISList {
			return menuAISServer
		}
		return menuADSBHost
	}

	// Long-press exit: MENU or START held for 3s quits; a short MENU tap
	// opens/closes the settings menu.
	var menuDownAt, startDownAt time.Time
	exitHint := ""
	// MENU+START held together = screenshot (any screen). Latches once
	// per joint press; menuInCombo suppresses Menu's short-tap toggle on
	// release so the combo doesn't also open the settings menu.
	shotCombo := false
	menuInCombo := false
	// MENU+Vol = backlight combo (launcher style). While active the
	// MENU hold-to-exit check is suspended — adjusting brightness must
	// never quit the app.
	var brightComboUntil time.Time

	adjustItem := func(idx, dir int) {
		switch idx {
		case menuMode:
			if r.FT8Enabled() {
				// USB-only while FT8 decodes (radio.SetMode enforces it;
				// skip the switch dance so the bw cfg isn't crossed).
				capturedMsg, capturedAt = i18n.T("ft8_modelock"), time.Now()
				return
			}
			saveBwNow(cfg, r)
			m := dsp.NextMode(r.Mode())
			if dir < 0 {
				// one back in a 5-cycle == four forward
				m = dsp.NextMode(dsp.NextMode(dsp.NextMode(dsp.NextMode(r.Mode()))))
			}
			r.SetMode(m)
			autoStep(m)
		case menuGain:
			r.SetGainDb(radio.GainStepDb(r.GainDb(), dir))
		case menuSQL:
			// Absolute dBFS, 2 dB per press, −100..0 (0 = off).
			db := r.SquelchDb() + float64(dir)*2
			if db < -100 {
				db = -100
			}
			if db > 0 {
				db = 0
			}
			r.SetSquelchDb(db)
			cfg["sql"] = fmt.Sprintf("%g", r.SquelchDb())
		case menuAF:
			{
				hp, lp := r.AudioFilter()
				p := audioFilterPresets[nextAudioFilterPreset(hp, lp, dir)]
				r.SetAudioFilter("hp", p.hpHz)
				r.SetAudioFilter("lp", p.lpHz)
				cfg["hp"] = fmt.Sprintf("%d", p.hpHz)
				cfg["lp"] = fmt.Sprintf("%d", p.lpHz)
			}
		case menuLocalMute:
			r.SetLocalMute(!r.LocalMuted())
			cfg["lmute"] = map[bool]string{true: "on", false: "off"}[r.LocalMuted()]
		case menuNR:
			r.SetNoiseReduction(r.NoiseReduction() + dir)
			cfg["nr"] = fmt.Sprintf("%d", r.NoiseReduction())
		case menuHP:
			hp, _ := r.AudioFilter()
			r.SetAudioFilter("hp", stepHzOption(hp, dir, audioHpSteps))
			cfg["hp"] = fmt.Sprintf("%d", hp2(r))
		case menuLP:
			_, lp := r.AudioFilter()
			r.SetAudioFilter("lp", stepHzOption(lp, dir, audioLpSteps))
			cfg["lp"] = fmt.Sprintf("%d", lp2(r))
		case menuSample:
			// RTL-SDR hardware rates our DSP supports (divisible by
			// 64 kHz for the SSB decimation chain). 256 kHz is the
			// bandwidth-saving option for mobile hotspots (0.5 MB/s);
			// 640 kHz was dropped — this server streams it broken.
			// The change reconnects with the new rate as the
			// connection's first command.
			rates := []int{256_000, 1_024_000, 1_536_000, 1_792_000, 2_048_000, 2_400_000, 2_560_000, 2_880_000, 3_200_000}
			cur := r.IQRate()
			idx := 0
			for i, v := range rates {
				if v == cur {
					idx = i
					break
				}
			}
			next := rates[(idx+len(rates)+dir)%len(rates)]
			r.SetCaptureRate(next)
		case menuBW:
			bws := r.Bandwidths()
			if len(bws) == 0 {
				return
			}
			cur := r.Bandwidth()
			idx := 0
			for i, v := range bws {
				if v == cur {
					idx = i
					break
				}
			}
			idx = (idx + len(bws) + dir) % len(bws)
			r.SetBandwidth(bws[idx])
			saveBwNow(cfg, r)
		case menuPPM:
			// Cycle −120…−1, OFF, 0…+120 — OFF sits one step below
			// zero (hands-off: a server with its own calibration is
			// left untouched, nothing is ever sent).
			if r.PpmOff() {
				if dir > 0 {
					r.SetPpmOff(false)
					r.SetPpm(0)
				} else {
					r.SetPpmOff(false)
					r.SetPpm(-1)
				}
			} else {
				v := r.Ppm()
				if dir > 0 {
					if v == -1 {
						r.SetPpmOff(true) // −1 → OFF → 0
					} else if v >= 120 {
						r.SetPpm(-120) // wrap past the top
					} else {
						r.SetPpm(v + 1)
					}
				} else {
					if v == 0 {
						r.SetPpmOff(true) // 0 → OFF → −1
					} else if v <= -120 {
						r.SetPpm(120) // wrap past the bottom
					} else {
						r.SetPpm(v - 1)
					}
				}
			}
			if r.PpmOff() {
				cfg["ppm"] = "off"
			} else {
				cfg["ppm"] = fmt.Sprintf("%d", r.Ppm())
			}
		case menuDS:
			// -1 auto → 2 on → 0 off → back to auto.
			switch r.DirectSamplingMode() {
			case -1:
				r.SetDirectSamplingMode(2)
			case 2:
				r.SetDirectSamplingMode(0)
			default:
				r.SetDirectSamplingMode(-1)
			}
		case menuFT8:
			if !r.FT8Enabled() {
				saveBwNow(cfg, r) // keep old mode's bw before the USB jump
				autoStep(dsp.ModeUSB)
			}
			r.SetFT8Enabled(!r.FT8Enabled())
		case menuAGC:
			r.SetAGCEnabled(!r.AGCEnabled())
		case menuHost:
		case menuWeb:
			on := !webSrv.Enabled()
			webSrv.SetEnabled(on)
			cfg["web"] = map[bool]string{true: "on", false: "off"}[on]
		case menuLang:
			if i18n.Lang() == "th" {
				i18n.SetLang("en")
			} else {
				i18n.SetLang("th")
			}
		case menuWebPort:
			p := webSrv.Port() + dir
			if p < 1024 {
				p = 1024
			}
			if p > 65535 {
				p = 65535
			}
			webSrv.SetPort(p)
			cfg["webport"] = fmt.Sprintf("%d", p)
		case menuSpan:
			spanStep(dir)
		case menuStep:
			for i, s := range stepSteps {
				if s == stepHz {
					stepHz = stepSteps[(i+len(stepSteps)+dir)%len(stepSteps)]
					break
				}
			}
			cfg["step"] = strconv.FormatInt(stepHz, 10)
		case menuWFMin:
			wfMu.Lock()
			wfMin += float64(dir)
			if wfMin < 0 {
				wfMin = 0
			}
			if wfMin > 40 {
				wfMin = 40
			}
			wfMu.Unlock()
			u.SetWaterfallRange(wfMin, wfMax)
			cfg["wfmin"] = fmt.Sprintf("%g", wfMin)
		case menuWFMax:
			wfMu.Lock()
			wfMax += float64(dir) * 2
			if wfMax < 10 {
				wfMax = 10
			}
			if wfMax > 120 {
				wfMax = 120
			}
			wfMu.Unlock()
			u.SetWaterfallRange(wfMin, wfMax)
			cfg["wfmax"] = fmt.Sprintf("%g", wfMax)
		case menuVolume:
			v := math.Round((r.Volume()+float64(dir)*0.01)*100) / 100
			if v < 0 {
				v = 0
			}
			if v > 1.5 {
				v = 1.5
			}
			r.SetVolume(v)
		case menuNRNN:
			on, mode := r.NREnabled()
			next := "voice"
			if on && mode == "voice" {
				next = "cw"
			} else if on && mode == "cw" {
				next = ""
			}
			if dir < 0 {
				if !on {
					next = "cw"
				} else if mode == "cw" {
					next = "voice"
				} else {
					next = ""
				}
			}
			if next == "" {
				r.SetNREnabled(false, "")
				cfg["nrnn"] = "off"
			} else if r.SetNREnabled(true, next) {
				cfg["nrnn"] = next
			} else {
				setMsg(i18n.T("nr_missing"))
			}
			saveNow()
		case menuAPRSBeacon:
			aprsBeaconIdx = (aprsBeaconIdx + dir + 7) % 7
			cfg["aprsbeacon"] = []string{"off", "1", "2", "5", "10", "30", "smart"}[aprsBeaconIdx]
			saveNow()
		case menuAPRSSym:
			aprsSymIdx = (aprsSymIdx + dir + len(aprsSymList)) % len(aprsSymList)
			e := aprsSymList[aprsSymIdx]
			cfg["aprssym"] = fmt.Sprintf("%c%c", e.table, e.sym)
			saveNow()
		case menuAPRSPre:
			aprsPre = math.Round((aprsPre+float64(dir)*0.1)*10) / 10
			if aprsPre < 0.1 {
				aprsPre = 0.1
			}
			if aprsPre > 2.0 {
				aprsPre = 2.0
			}
			cfg["aprspre"] = fmt.Sprintf("%.1f", aprsPre)
			saveNow()
		case menuAPRSGateLim:
			aprsGateLim += dir
			if aprsGateLim < 1 {
				aprsGateLim = 1
			}
			if aprsGateLim > 60 {
				aprsGateLim = 60
			}
			cfg["aprsigatelimit"] = fmt.Sprintf("%d", aprsGateLim)
			saveNow()
		case menuAPRSLvl:
			aprsLvl += dir * 10
			if aprsLvl < 0 {
				aprsLvl = 0
			}
			if aprsLvl > 100 {
				aprsLvl = 100
			}
			cfg["aprslvl"] = fmt.Sprintf("%d", aprsLvl)
			saveNow()
		case menuPSK:
			pskOn = !pskOn
			cfg["psk"] = map[bool]string{true: "on", false: "off"}[pskOn]
			psk.SetEnabled(pskOn)
		}
	}
	activateItem := func(idx int) {
		if menuPage == pageRoot {
			switch rootRowAction(menuSel, idx) {
			case rootExit:
				close(exitMenu)
				return
			case rootBookmarks:
				bmSel = 0
				uiMode = uiBmList
				return
			case rootPage:
				menuPage = rootRowPage(menuSel)
				menuSel = 0
				return
			}
		}
		switch idx {
		case menuFT8:
			if !r.FT8Enabled() {
				saveBwNow(cfg, r) // keep old mode's bw before the USB jump
				autoStep(dsp.ModeUSB)
			}
			r.SetFT8Enabled(!r.FT8Enabled())
		case menuLang:
			if i18n.Lang() == "th" {
				i18n.SetLang("en")
			} else {
				i18n.SetLang("th")
			}
		case menuCall:
			hostText, kbTarget = myCall, "call"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuGrid:
			hostText, kbTarget = myGrid, "grid"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAnt:
			hostText, kbTarget = myAnt, "ant"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuRig:
			hostText, kbTarget = myRig, "rig"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuPSK:
			pskOn = !pskOn
			cfg["psk"] = map[bool]string{true: "on", false: "off"}[pskOn]
			psk.SetEnabled(pskOn)
		case menuRTTY:
			r.SetRTTYEnabled(!r.RTTYEnabled())
			cfg["rtty"] = map[bool]string{true: "on", false: "off"}[r.RTTYEnabled()]
			fmt.Fprintf(os.Stderr, "rtty: decode %s\n", cfg["rtty"])
		case menuRTTYLog:
			rttyScroll = 0
			uiMode = uiRTTY
		case menuWefax:
			r.SetWefaxEnabled(!r.WefaxEnabled())
			cfg["wefax"] = map[bool]string{true: "on", false: "off"}[r.WefaxEnabled()]
			saveNow()
			fmt.Fprintf(os.Stderr, "wefax: decode %s\n", cfg["wefax"])
		case menuWefaxClear:
			r.Wefax().Clear()
			setMsg(i18n.T("wefax_cleared"))
		case menuWefaxAuto:
			r.SetWefaxAutoSave(!r.WefaxAutoSave())
			cfg["wefaxauto"] = map[bool]string{true: "on", false: "off"}[r.WefaxAutoSave()]
			saveNow()
		case menuCWDec:
			r.SetCWDecodeEnabled(!r.CWDecodeEnabled())
			cfg["cwdec"] = map[bool]string{true: "on", false: "off"}[r.CWDecodeEnabled()]
			saveNow()
			fmt.Fprintf(os.Stderr, "cw: decode %s\n", cfg["cwdec"])
		case menuCWClear:
			r.CW().Clear()
		case menuDeepCW:
			if r.DeepCWEnabled() {
				r.SetDeepCWEnabled(false)
				cfg["deepcw"] = "off"
				saveNow()
				break
			}
			if !r.DeepCWAvailable() {
				// First enable: fetch the ~40 MB sidecar bundle in the
				// background, then start it.
				setMsg(i18n.T("deepcw_fetching"))
				go func() {
					fetchDeepCWBundle(defaultUpdateBase, filepath.Dir(mustExe()))
					if r.SetDeepCWEnabled(true) {
						cfg["deepcw"] = "on"
						saveNow()
					} else {
						setMsg(i18n.T("deepcw_missing"))
					}
				}()
				break
			}
			if r.SetDeepCWEnabled(true) {
				cfg["deepcw"] = "on"
			} else {
				cfg["deepcw"] = "off"
				setMsg(i18n.T("deepcw_missing"))
			}
			saveNow()
		case menuDeepCWThreads:
			t, w := r.DeepCWParams()
			t = t%4 + 1
			r.SetDeepCWParams(t, w)
			cfg["deepcwth"] = fmt.Sprintf("%d", t)
			saveNow()
		case menuDeepCWWindow:
			t, w := r.DeepCWParams()
			idx := 0
			for i, v := range deepcwWindows {
				if v == w {
					idx = i
				}
			}
			w = deepcwWindows[(idx+1)%len(deepcwWindows)]
			r.SetDeepCWParams(t, w)
			cfg["deepcwwin"] = fmt.Sprintf("%d", w)
			saveNow()
		case menuDeepCWClear:
			r.DeepCWClear()
		case menuFT8TS:
			if r.FT8TSEnabled() {
				r.SetFT8TSEnabled(false)
				cfg["ft8ts"] = "off"
				saveNow()
				break
			}
			if !r.FT8TSAvailable() {
				setMsg(i18n.T("ft8ts_fetching"))
				go func() {
					fetchFT8TSBundle(defaultUpdateBase, filepath.Dir(mustExe()))
					if r.SetFT8TSEnabled(true) {
						cfg["ft8ts"] = "on"
						saveNow()
					} else {
						setMsg(i18n.T("ft8ts_missing"))
					}
				}()
				break
			}
			if r.SetFT8TSEnabled(true) {
				cfg["ft8ts"] = "on"
			} else {
				cfg["ft8ts"] = "off"
				setMsg(i18n.T("ft8ts_missing"))
			}
			saveNow()
		case menuFT8TSDepth:
			d, t, lo, hi := r.FT8TSParams()
			d = d%3 + 1
			r.SetFT8TSParams(d, t, lo, hi)
			cfg["ft8tsdepth"] = fmt.Sprintf("%d", d)
			saveNow()
		case menuFT8TSThreads:
			d, t, lo, hi := r.FT8TSParams()
			t = t%4 + 1
			r.SetFT8TSParams(d, t, lo, hi)
			cfg["ft8tsth"] = fmt.Sprintf("%d", t)
			saveNow()
		case menuFT8TSBand:
			d, t, lo, hi := r.FT8TSParams()
			if lo < 50 {
				lo, hi = 200, 3000
			}
			idx := 0
			for i, b := range ft8tsBands {
				if b[0] == lo && b[1] == hi {
					idx = i
				}
			}
			nb := ft8tsBands[(idx+1)%len(ft8tsBands)]
			r.SetFT8TSParams(d, t, nb[0], nb[1])
			cfg["ft8tsband"] = fmt.Sprintf("%d-%d", nb[0], nb[1])
			saveNow()
		case menuADSBHost:
			beastSel = 0
			uiMode = uiBeastList
		case menuADSBLat:
			hostText, kbTarget = fmt.Sprintf("%.5f", adsbLat), "adsblat"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuADSBLon:
			hostText, kbTarget = fmt.Sprintf("%.5f", adsbLon), "adsblon"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuADSBRadar:
			panX, panY = 0, 0
			radarSel.On = false
			radarSel.ID = ""
			uiMode = uiADSB
		case menuAPRSRx:
			on := !r.APRSEnabled()
			r.SetAPRSEnabled(on)
			cfg["aprs"] = map[bool]string{true: "on", false: "off"}[on]
			saveNow()
			r.SetAPRSFreq(aprsFreq)
		case menuAPRSFreq:
			hostText, kbTarget = fmt.Sprintf("%.4f", float64(aprsFreq)/1e6), "aprsfreq"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSCall:
			hostText, kbTarget = aprsCall, "aprscall"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSIS:
			aprsISOn = !aprsISOn
			cfg["aprsis"] = map[bool]string{true: "on", false: "off"}[aprsISOn]
			saveNow()
		case menuAPRSServer:
			hostText, kbTarget = aprsIServer, "aprsiserver"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSPath:
			hostText, kbTarget = aprsPath, "aprspath"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSCmt:
			hostText, kbTarget = aprsCmt, "aprscmt"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSStat:
			panX, panY = 0, 0
			radarSel.On = false
			radarSel.ID = ""
			uiMode = uiADSB
		case menuSSTV:
			r.SetSSTVEnabled(!r.SSTVEnabled())
			cfg["sstv"] = map[bool]string{true: "on", false: "off"}[r.SSTVEnabled()]
			saveNow()
		case menuSSTVView:
			uiMode = uiSSTV
		case menuSSTVClear:
			r.SSTV().Clear()
		case menuAPRSLog:
			aprsLogScroll = 0
			uiMode = uiAPRSLog
		case menuAPRSIgate:
			aprsIgateOn = !aprsIgateOn
			cfg["aprsigate"] = map[bool]string{true: "on", false: "off"}[aprsIgateOn]
			saveNow()
		case menuAPRSSrc:
			if aprsSrc == "gps" {
				aprsSrc = "fix"
			} else {
				aprsSrc = "gps"
			}
			cfg["aprssrc"] = aprsSrc
			saveNow()
		case menuAPRSFixLat:
			hostText, kbTarget = fmt.Sprintf("%.5f", aprsFixLat), "aprsfixlat"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSFixLon:
			hostText, kbTarget = fmt.Sprintf("%.5f", aprsFixLon), "aprsfixlon"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAPRSNow:
			sendAPRSNow(r, gpsRx, aprsCall, aprsPath, aprsSymList[aprsSymIdx], aprsCmt, aprsLvl, aprsPre, aprsISOn, aprsIServer, aprsSrc, aprsFixLat, aprsFixLon, aprsStore, &aprsLastBeacon, &aprsLastCourse, setMsg)
		case menuAISRF:
			on := !r.AISRFEnabled()
			r.SetAISRFEnabled(on)
			cfg["aisrf"] = map[bool]string{true: "on", false: "off"}[on]
		case menuADSBRF:
			on := !r.ADSBRFEnabled()
			r.SetADSBRFEnabled(on)
			cfg["adsbrf"] = map[bool]string{true: "on", false: "off"}[on]
			saveNow()
		case menuRTLSrv:
			if r.RTLSrvEnabled() {
				r.SetRTLSrvEnabled(false)
				cfg["rtlsrv"] = "off"
			} else {
				r.SetRTLSrvPort(rtlSrvPort)
				ok := r.SetRTLSrvEnabled(true)
				cfg["rtlsrv"] = map[bool]string{true: "on", false: "off"}[ok]
				if !ok {
					setMsg(i18n.T("rtlsrv_fail"))
				}
			}
			saveNow()
		case menuRTLSrvPort:
			hostText, kbTarget = fmt.Sprintf("%d", rtlSrvPort), "rtlsrvport"
			hostKbR, hostKbC = 0, 0
			uiMode = uiHostEdit
		case menuAISLog:
			aisScroll = 0
			uiMode = uiAISLog
		case menuClearMap:
			// Tiles live in osmcache/ next to the exe; OTA never touches
			// them, so this is the only way to start fresh.
			if err := osmCache.Clear(); err == nil {
				for i := range adsbMosaic {
					for j := range adsbMosaic[i] {
						adsbMosaic[i][j] = nil
					}
				}
				mapCacheClearedAt = time.Now()
			}
		case menuGPSFollow:
			gpsFollow = !gpsFollow
			cfg["gpsfollow"] = map[bool]string{true: "on", false: "off"}[gpsFollow]
			saveNow()
		case menuGPSTimeSync:
			gpsTimeSync = !gpsTimeSync
			cfg["gpstime"] = map[bool]string{true: "on", false: "off"}[gpsTimeSync]
			gpsLastClockCheck = time.Now().Add(-time.Hour) // re-arm
			saveNow()
		case menuAISServer:
			aisSel = 0
			uiMode = uiAISList
		case menuSysMon:
			uiMode = uiSysMon
		case menuLogs:
			uiMode = uiLogs
		case menuBM:
			bmSel = 0
			uiMode = uiBmList
		case menuMap:
			mapSel, mapDetail = -1, false
			uiMode = uiMap
		case menuBands:
			ft8BandSel = 0
			uiMode = uiFT8Bands
		case menuHost:
			hostSel = 0
			uiMode = uiHostList
		case menuFreq:
			editDigits = freqDigits()
			uiMode = uiFreqEdit
		case menuShot:
			capture()
		case menuUpdate:
			runUpdate(upd, updateBase, true, saveNow)
		default:
			adjustItem(idx, +1)
		}
	}
	commitFreq := func(digits string) {
		var mhz, frac int
		fmt.Sscanf(digits[:4], "%d", &mhz)
		fmt.Sscanf(digits[4:], "%d", &frac)
		hz := int64(mhz)*1_000_000 + int64(frac)*10
		if hz < 500_000 {
			hz = 500_000
		}
		if hz > 1_766_000_000 {
			hz = 1_766_000_000
		}
		r.SetFreq(hz)
	}

	act := func(b input.Button) {
		switch b {
		case input.Left:
			r.SetFreq(r.Freq() - stepFor())
		case input.Right:
			r.SetFreq(r.Freq() + stepFor())
		case input.Up:
			// D-pad up/down walk the frequency axis like a list
			// (down = next/higher), the same orientation as the
			// waterfall's frequency grid.
			r.SetFreq(r.Freq() - 10*stepFor())
		case input.Down:
			r.SetFreq(r.Freq() + 10*stepFor())
		case input.A:
			if r.FT8Enabled() {
				// Mode cycling disabled while FT8 decodes — USB only
				// (radio.SetMode would reject it anyway; bail out before
				// the bw/step dance crosses settings between modes).
				capturedMsg, capturedAt = i18n.T("ft8_modelock"), time.Now()
				return
			}
			// Save current mode's bandwidth before switching.
			saveBwNow(cfg, r)
			m := dsp.NextMode(r.Mode())
			r.SetMode(m)
			if bw, ok := cfg[fmt.Sprintf("bw.%s", m.Name)]; ok {
				if v, err := strconv.ParseFloat(bw, 64); err == nil {
					r.SetBandwidth(v)
				}
			}
			autoStep(m)
		case input.Select:
			// Big scrollable history window (FT8 or, when the AIS RF
			// decoder runs, the AIS messages).
			if r.FT8Enabled() {
				ft8Scroll = 0
				uiMode = uiFT8Log
			} else if r.AISRFEnabled() {
				aisScroll = 0
				uiMode = uiAISLog
			}
		case input.Y:
			if !r.FT8Enabled() && r.WefaxEnabled() {
				saveWefax()
				return
			}
			if r.FT8Enabled() {
				r.SyncFT8()
				capturedMsg = i18n.T("ft8_synced")
				capturedAt = time.Now()
				// Slot boundaries for the red waterfall markers: Y is
				// pressed when a slot starts, so boundaries run every
				// 15 s from now (-1 draws one immediately).
				ft8SyncWall = time.Now()
				ft8SlotIdx = -1
			}
		case input.X:
			r.CycleSquelch()
			cfg["sql"] = fmt.Sprintf("%g", r.SquelchDb())
		case input.L1:
			// Zoom out: next wider waterfall span (same list + live
			// anchoring as the menu row; persisted with the live value).
			spanStep(1)
			capturedMsg, capturedAt = fmt.Sprintf(i18n.T("span_fmt"), u.SpanFull/1000), time.Now()
		case input.R1:
			// Zoom in: next narrower span.
			spanStep(-1)
			capturedMsg, capturedAt = fmt.Sprintf(i18n.T("span_fmt"), u.SpanFull/1000), time.Now()
		case input.L2, input.R2:
			// Tune step: R2 coarser, L2 finer (same ascending list +
			// wrap + persistence as the menu row).
			dir := 1
			if b == input.L2 {
				dir = -1
			}
			for i, s := range stepSteps {
				if s == stepHz {
					stepHz = stepSteps[(i+len(stepSteps)+dir)%len(stepSteps)]
					break
				}
			}
			cfg["step"] = strconv.FormatInt(stepHz, 10)
			setMsg(fmt.Sprintf(i18n.T("step_fmt"), stepLabel(stepHz)))
		}
	}
	handlePress := func(b input.Button) {
		switch uiMode {
		case uiMain:
			act(b)
		case uiMenu:
			rows := len(pageItems[menuPage])
			switch b {
			case input.Up:
				menuSel = (menuSel + rows - 1) % rows
			case input.Down:
				menuSel = (menuSel + 1) % rows
			case input.Left:
				if menuPage != pageRoot {
					adjustItem(pageItems[menuPage][menuSel], -1)
				}
			case input.Right:
				if menuPage != pageRoot {
					adjustItem(pageItems[menuPage][menuSel], +1)
				}
			case input.A:
				activateItem(pageItems[menuPage][menuSel])
			case input.B, input.Start:
				if menuPage != pageRoot {
					menuPage, menuSel = pageRoot, 0
				} else {
					uiMode = uiMain
				}
			}
		case uiFT8Log:
			// D-pad scrolls the big FT8 history: up/down one line,
			// left/right one page (8 lines).
			page := 8
			switch b {
			case input.Up:
				ft8Scroll += 1
			case input.Down:
				ft8Scroll -= 1
			case input.Left:
				ft8Scroll += page
			case input.Right:
				ft8Scroll -= page
			case input.B, input.Start, input.Select:
				uiMode = uiMain
			}
			if ft8Scroll > len(ft8Log) {
				ft8Scroll = len(ft8Log)
			}
			if ft8Scroll < 0 {
				ft8Scroll = 0
			}
		case uiSysMon:
			// Any of the usual close keys backs out of the monitor.
			switch b {
			case input.B, input.Start, input.Select, input.A:
				uiMode, menuPage, menuSel = uiMenu, pageSys, 2
			}
		case uiBmList:
			// Rows: saved bookmarks + "save current" at the bottom.
			rows := len(bookmarks) + 1
			switch b {
			case input.Up:
				bmSel = (bmSel + rows - 1) % rows
			case input.Down:
				bmSel = (bmSel + 1) % rows
			case input.A:
				if bmSel == len(bookmarks) {
					// Save current freq+mode as a new bookmark.
					label := fmt.Sprintf("%.4f MHz", float64(r.Freq())/1e6)
					bookmarks = append(bookmarks, bmT{freqHz: r.Freq(), mode: r.Mode().Name, label: label})
					saveBookmarks()
					bmSel = len(bookmarks) - 1
				} else {
					bm := bookmarks[bmSel]
					r.SetFreq(bm.freqHz)
					m := dsp.ModeByName(bm.mode)
					r.SetMode(m)
					autoStep(m)
					uiMode = uiMain
				}
			case input.X:
				if bmSel < len(bookmarks) {
					hostText, kbTarget = bookmarks[bmSel].label, "bm"
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				}
			case input.Y:
				if bmSel < len(bookmarks) {
					bookmarks = append(bookmarks[:bmSel], bookmarks[bmSel+1:]...)
					if bmSel >= len(bookmarks) {
						bmSel = len(bookmarks)
					}
					saveBookmarks()
				}
			case input.B, input.Start:
				uiMode, menuPage, menuSel = uiMenu, pageRoot, 8
			}
		case uiLogs:
			// d-pad scrolls (line/page), close keys back to the menu.
			switch b {
			case input.Up:
				logScroll++
			case input.Down:
				logScroll--
			case input.Left:
				logScroll += 16
			case input.Right:
				logScroll -= 16
			case input.B, input.Start, input.Select, input.A:
				uiMode, menuPage, menuSel = uiMenu, pageSys, 3
			}
			if logScroll > 5000 {
				logScroll = 5000
			}
			if logScroll < 0 {
				logScroll = 0
			}
		case uiMap:
			// Map screen: L1/R1 cycle the basemap style, L2/R2 the
			// overlay colour set, Left/Right walk the sorted station
			// list, A toggles the info panel, B backs out stepwise
			// (panel → selection → close).
			switch b {
			case input.L1:
				u.CycleMap(-1)
			case input.R1:
				u.CycleMap(1)
			case input.L2:
				u.CycleMapPalette(-1)
			case input.R2:
				u.CycleMapPalette(1)
			case input.Left:
				if n := len(mapStationNames); n > 0 {
					mapDetail = false
					if mapSel < 0 {
						mapSel = n - 1
					} else {
						mapSel = (mapSel + n - 1) % n
					}
				}
			case input.Right:
				if n := len(mapStationNames); n > 0 {
					mapDetail = false
					if mapSel < 0 {
						mapSel = 0
					} else {
						mapSel = (mapSel + 1) % n
					}
				}
			case input.A:
				if mapSel >= 0 && mapSel < len(mapStationNames) {
					mapDetail = !mapDetail
				}
			case input.B:
				if mapDetail {
					mapDetail = false
				} else if mapSel >= 0 {
					mapSel = -1
				} else {
					mapSel, mapDetail = -1, false
					uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuMap)
				}
			case input.Start, input.Select:
				mapSel, mapDetail = -1, false
				uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuMap)
			}
		case uiADSB:
			// Radar: L2/R2 cycle the map layer, L1/R1 the range
			// (swapped per user request — zoom on the shoulder the
			// thumb rests on), the d-pad pans the map, B/Start back.
			switch b {
			case input.L1, input.R1:
				// Zoom keeps the geographic point at screen centre
				// fixed: the pan (screen px at the old zoom) is
				// re-expressed at the new zoom around that anchor.
				oldZ := adsbZooms[adsbRangeIdx]
				if b == input.R1 {
					adsbRangeIdx = (adsbRangeIdx + len(adsbRanges) - 1) % len(adsbRanges)
				} else {
					adsbRangeIdx = (adsbRangeIdx + 1) % len(adsbRanges)
				}
				newZ := adsbZooms[adsbRangeIdx]
				if oldZ != newZ {
					rxo, ryo := osm.MercatorPx(adsbLat, adsbLon, oldZ)
					latC, lonC := osm.MercatorInv(rxo+float64(panX), ryo+float64(panY), oldZ)
					rxn, ryn := osm.MercatorPx(adsbLat, adsbLon, newZ)
					vxn, vyn := osm.MercatorPx(latC, lonC, newZ)
					panX, panY = int(math.Round(vxn-rxn)), int(math.Round(vyn-ryn))
				}
				adsbFreeMosaics()
			case input.L2:
				adsbLayerIdx = (adsbLayerIdx + len(osm.Layers) - 1) % len(osm.Layers)
				cfg["adsblayer"] = fmt.Sprintf("%d", adsbLayerIdx)
				adsbFreeMosaics()
			case input.R2:
				adsbLayerIdx = (adsbLayerIdx + 1) % len(osm.Layers)
				cfg["adsblayer"] = fmt.Sprintf("%d", adsbLayerIdx)
				adsbFreeMosaics()
			case input.Up:
				if radarSel.On {
					radarSel.Idx--
				} else {
					panY -= 80
				}
			case input.Down:
				if radarSel.On {
					radarSel.Idx++
				} else {
					panY += 80
				}
			case input.Left:
				if radarSel.On {
					radarSel.Idx--
				} else {
					panX -= 80
				}
			case input.Right:
				if radarSel.On {
					radarSel.Idx++
				} else {
					panX += 80
				}
			case input.A:
				// Flag+text → flag only → bare targets → both.
				radarLabelMode = (radarLabelMode + 1) % 3
				cfg["radarlabel"] = fmt.Sprintf("%d", radarLabelMode)
			case input.X:
				// Cycle the visibility bitmask: all → planes+ships →
				// planes → ships → APRS → planes+APRS → ships+APRS.
				order := []int{7, 3, 1, 2, 4, 5, 6}
				i := 0
				for k, v := range order {
					if v == radarTargetsMask {
						i = k
						break
					}
				}
				radarTargetsMask = order[(i+1)%len(order)]
				cfg["radartargets"] = fmt.Sprintf("%d", radarTargetsMask)
			case input.Y:
				// Ship name↔MMSI / aircraft callsign↔registration.
				aisShowName = !aisShowName
				cfg["aisname"] = fmt.Sprintf("%v", aisShowName)
			case input.Select:
				// Toggle the target selector: the d-pad then walks the
				// drawn blips and a detail panel opens on the opposite
				// half of the screen.
				if radarSel.On {
					radarSel.On = false
				} else {
					radarSel.On = true
					radarSel.Idx = 0
					radarSel.ID = "" // anchor at draw time
				}
			case input.Start:
				panX, panY = 0, 0 // recentre on the receiver
			case input.B:
				uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuADSBRadar)
			}
		case uiRTTY:
			// RTTY text screen: up/down scroll, Y reverses mark/space
			// polarity, X clears, B/Start back to the FT8 page.
			switch b {
			case input.Up:
				rttyScroll++
			case input.Down:
				rttyScroll--
			case input.Y:
				r.SetRTTYReversed(!r.RTTYReversed())
			case input.X:
				rttyLines = nil
				rttyScroll = 0
			case input.B, input.Start, input.Select:
				uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuRTTYLog)
			}
			if rttyScroll > 500 {
				rttyScroll = 500
			}
			if rttyScroll < 0 {
				rttyScroll = 0
			}
		case uiFT8Bands:
			// Band picker: up/down walk the list, A tunes there (and
			// turns FT8 decode on), B/Start back to the FT8 page.
			switch b {
			case input.Up:
				ft8BandSel = (ft8BandSel + len(ft8Bands) - 1) % len(ft8Bands)
			case input.Down:
				ft8BandSel = (ft8BandSel + 1) % len(ft8Bands)
			case input.A:
				band := ft8Bands[ft8BandSel]
				if !r.FT8Enabled() {
					saveBwNow(cfg, r) // keep old mode's bw before the USB jump
					autoStep(dsp.ModeUSB)
					r.SetFT8Enabled(true)
				}
				r.SetFreq(band.hz)
				capturedMsg = fmt.Sprintf("FT8 %s", band.label)
				capturedAt = time.Now()
				uiMode = uiMain
			case input.B, input.Start, input.Select:
				uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuBands)
			}
		case uiHostList:
			// Rows: a leading "(ปิดใช้งาน)" row (radio off — viewer
			// only), the local USB dongle, saved hosts, "add new" at the
			// bottom.
			rows := len(hostList) + 3
			switch b {
			case input.Up:
				hostSel = (hostSel + rows - 1) % rows
			case input.Down:
				hostSel = (hostSel + 1) % rows
			case input.A:
				if hostSel == 0 {
					// Radio off: no rtl_tcp connection, no IQ/decode.
					*host = ""
					cfg["host"] = "off"
					r.SetHost("")
					saveNow()
					uiMode = uiMenu
				} else if hostSel == 1 {
					// Local USB dongle: the app spawns its own rtl_tcp.
					*host = "usb"
					cfg["host"] = "usb"
					r.SetHost("usb")
					saveNow()
					uiMode = uiMenu
				} else if hostSel == len(hostList)+2 {
					hostText, hostEditIdx, kbTarget = "", -1, "host"
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				} else {
					h := hostList[hostSel-2]
					*host = h
					cfg["host"] = h
					r.SetHost(h)
					saveHosts()
					saveNow()
					uiMode = uiMenu
				}
			case input.X:
				if hostSel >= 2 && hostSel <= len(hostList)+1 {
					hostText, hostEditIdx, kbTarget = hostList[hostSel-2], hostSel-2, "host"
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				}
			case input.Y:
				if hostSel >= 2 && hostSel <= len(hostList)+1 {
					i := hostSel - 2
					hostList = append(hostList[:i], hostList[i+1:]...)
					if len(hostList) == 0 && *host != "" {
						hostList = []string{*host}
					}
					if hostSel > len(hostList)+2 {
						hostSel = len(hostList) + 2
					}
					saveHosts()
					saveNow()
				}
			case input.B, input.Start:
				uiMode = uiMenu
			}
		case uiAISLog:
			// AIS message screen: up/down scroll, B/Start back.
			switch b {
			case input.Up:
				aisScroll++
			case input.Down:
				aisScroll--
			case input.B, input.Start, input.Select:
				uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuAISLog)
			}
		case uiSSTV:
			switch b {
			case input.B, input.Start, input.Select:
				uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuSSTVView)
			}
		case uiAPRSLog:
			switch b {
			case input.Up:
				aprsLogScroll++
			case input.Down:
				aprsLogScroll--
			case input.L1, input.Left:
				aprsLogTab = (aprsLogTab + 2) % 3
				aprsLogScroll = 0
			case input.R1, input.Right:
				aprsLogTab = (aprsLogTab + 1) % 3
				aprsLogScroll = 0
			case input.B, input.Start, input.Select:
				uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSLog)
			}
		case uiBeastList, uiAISList:
			// Same rows/select/edit/delete mechanics as the radio host
			// list, aimed at the Beast or AIS target — plus a leading
			// "disabled" row that disconnects the feed entirely (RF
			// decode only).
			list := &beastList
			sel := &beastSel
			if uiMode == uiAISList {
				list, sel = &aisList, &aisSel
			}
			rows := len(*list) + 2 // disable row + add row
			switch b {
			case input.Up:
				*sel = (*sel + rows - 1) % rows
			case input.Down:
				*sel = (*sel + 1) % rows
			case input.A:
				if *sel == 0 {
					// Disable: an empty host keeps the client idle.
					if uiMode == uiAISList {
						aisHost = ""
						cfg["aishost"] = "off"
						aisClient.SetHost("")
					} else {
						adsbHost = ""
						cfg["adsbhost"] = "off"
						adsbClient.SetHost("")
					}
					saveNow()
					uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuBeastRow(uiMode))
				} else if *sel == len(*list)+1 {
					hostText, hostEditIdx, kbTarget = "", -1, listTargetOf(uiMode)
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				} else {
					h := (*list)[*sel-1]
					if uiMode == uiAISList {
						aisHost = h
						cfg["aishost"] = h
						aisClient.SetHost(h)
						saveAISHosts()
					} else {
						adsbHost = h
						cfg["adsbhost"] = h
						adsbClient.SetHost(h)
						saveBeastHosts()
					}
					saveNow() // lists must survive a hard power-off
					uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuBeastRow(uiMode))
				}
			case input.X:
				if *sel >= 1 && *sel <= len(*list) {
					hostText, hostEditIdx, kbTarget = (*list)[*sel-1], *sel-1, listTargetOf(uiMode)
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				}
			case input.Y:
				if *sel >= 1 && *sel <= len(*list) {
					i := *sel - 1
					*list = append((*list)[:i], (*list)[i+1:]...)
					if len(*list) == 0 {
						fallback := adsbHost
						if uiMode == uiAISList {
							fallback = aisHost
						}
						if fallback != "" {
							*list = []string{fallback}
						}
					}
					if *sel > len(*list)+1 {
						*sel = len(*list) + 1
					}
					if uiMode == uiBeastList {
						saveBeastHosts()
					} else {
						saveAISHosts()
					}
					saveNow()
				}
			case input.B, input.Start:
				uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuBeastRow(uiMode))
			}
		case uiHostEdit:
			switch b {
			case input.Up:
				hostKbR = (hostKbR + len(kbRows) - 1) % len(kbRows)
				if hostKbC >= len(kbRows[hostKbR]) {
					hostKbC = len(kbRows[hostKbR]) - 1
				}
			case input.Down:
				hostKbR = (hostKbR + 1) % len(kbRows)
				if hostKbC >= len(kbRows[hostKbR]) {
					hostKbC = len(kbRows[hostKbR]) - 1
				}
			case input.Left:
				hostKbC = (hostKbC + len(kbRows[hostKbR]) - 1) % len(kbRows[hostKbR])
			case input.Right:
				hostKbC = (hostKbC + 1) % len(kbRows[hostKbR])
			case input.A:
				if len(hostText) < 60 {
					ch := kbRows[hostKbR][hostKbC]
					if kbShifted && ch >= 'a' && ch <= 'z' {
						ch = ch - 32 // uppercase
					}
					hostText += string(ch)
				}
			case input.L2:
				kbShifted = !kbShifted
			case input.B:
				if len(hostText) > 0 {
					hostText = hostText[:len(hostText)-1]
				}
			case input.X, input.Y:
				switch kbTarget {
				case "aprscall":
					if hostText != "" {
						aprsCall = strings.ToUpper(strings.TrimSpace(hostText))
						cfg["aprscall"] = aprsCall
						saveNow()
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSCall)
				case "aprsfixlat":
					if v, err := strconv.ParseFloat(strings.TrimSpace(hostText), 64); err == nil && v >= -90 && v <= 90 {
						aprsFixLat = v
						cfg["aprsfixlat"] = fmt.Sprintf("%.5f", v)
						saveNow()
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSFixLat)
				case "aprsfixlon":
					if v, err := strconv.ParseFloat(strings.TrimSpace(hostText), 64); err == nil && v >= -180 && v <= 180 {
						aprsFixLon = v
						cfg["aprsfixlon"] = fmt.Sprintf("%.5f", v)
						saveNow()
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSFixLon)
				case "rtlsrvport":
					if v, err := strconv.Atoi(strings.TrimSpace(hostText)); err == nil && v >= 1 && v <= 65535 {
						rtlSrvPort = v
						cfg["rtlsrvport"] = fmt.Sprintf("%d", v)
						if r.RTLSrvEnabled() { // restart on the new port
							r.SetRTLSrvEnabled(false)
							r.SetRTLSrvPort(v)
							r.SetRTLSrvEnabled(true)
						}
						saveNow()
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuRTLSrvPort)
				case "aprsiserver":
					if t := strings.TrimSpace(hostText); strings.Contains(t, ":") {
						aprsIServer = t
						cfg["aprsiserver"] = t
						saveNow()
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSServer)
				case "aprspath":
					aprsPath = strings.ToUpper(strings.TrimSpace(hostText))
					if aprsPath == "" {
						aprsPath = "WIDE1-1,WIDE2-1"
					}
					cfg["aprspath"] = aprsPath
					saveNow()
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSPath)
				case "aprscmt":
					aprsCmt = hostText
					cfg["aprscmt"] = aprsCmt
					saveNow()
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSCmt)
				case "aprsfreq":
					if v, err := strconv.ParseFloat(strings.TrimSpace(hostText), 64); err == nil && v > 0.5 && v < 1766 {
						aprsFreq = int64(v * 1e6)
						cfg["aprsfreq"] = fmt.Sprintf("%.4f", float64(aprsFreq)/1e6)
						saveNow()
						r.SetAPRSFreq(aprsFreq)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageAPRS, menuRow(pageAPRS, menuAPRSFreq)
				case "call":
					if hostText != "" {
						myCall = strings.ToUpper(hostText)
						cfg["call"] = myCall
						psk.SetStation(myCall, myGrid, myAnt, myRig)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuCall)
				case "grid":
					if hostText != "" {
						myGrid = strings.ToUpper(hostText)
						cfg["grid"] = myGrid
						psk.SetStation(myCall, myGrid, myAnt, myRig)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuGrid)
				case "ant":
					myAnt = hostText
					cfg["antenna"] = myAnt
					psk.SetStation(myCall, myGrid, myAnt, myRig)
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuAnt)
				case "rig":
					myRig = hostText
					cfg["rig"] = myRig
					psk.SetStation(myCall, myGrid, myAnt, myRig)
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageStation, menuRow(pageStation, menuRig)
				case "bm":
					if bmSel < len(bookmarks) {
						bookmarks[bmSel].label = hostText
						saveBookmarks()
					}
					hostText = ""
					uiMode = uiBmList
				case "beast":
					if hostText != "" {
						if hostEditIdx >= 0 && hostEditIdx < len(beastList) {
							beastList[hostEditIdx] = hostText
						} else {
							beastList = append(beastList, hostText)
							hostEditIdx = len(beastList) - 1
						}
						saveBeastHosts()
						saveNow()
						adsbHost = hostText
						cfg["adsbhost"] = adsbHost
						adsbClient.SetHost(adsbHost)
						beastSel = hostEditIdx
					}
					hostEditIdx = -1
					hostText = ""
					uiMode = uiBeastList
				case "adsblat":
					if v, err := strconv.ParseFloat(hostText, 64); err == nil {
						adsbLat = v
						cfg["adsblat"] = fmt.Sprintf("%.5f", adsbLat)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuADSBLat)
				case "adsblon":
					if v, err := strconv.ParseFloat(hostText, 64); err == nil {
						adsbLon = v
						cfg["adsblon"] = fmt.Sprintf("%.5f", adsbLon)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageADSB, menuRow(pageADSB, menuADSBLon)
				case "ais":
					if hostText != "" {
						if hostEditIdx >= 0 && hostEditIdx < len(aisList) {
							aisList[hostEditIdx] = hostText
						} else {
							aisList = append(aisList, hostText)
							hostEditIdx = len(aisList) - 1
						}
						saveAISHosts()
						saveNow()
						aisHost = hostText
						cfg["aishost"] = aisHost
						aisClient.SetHost(aisHost)
						aisSel = hostEditIdx
					}
					hostEditIdx = -1
					hostText = ""
					uiMode = uiAISList

				default:
					if hostText != "" {
						if hostEditIdx >= 0 && hostEditIdx < len(hostList) {
							hostList[hostEditIdx] = hostText
						} else {
							hostList = append(hostList, hostText)
							hostEditIdx = len(hostList) - 1
						}
						saveHosts()
						*host = hostText
						cfg["host"] = hostText
						r.SetHost(hostText)
						hostSel = hostEditIdx
						saveNow()
					}
					hostEditIdx = -1
					uiMode = uiHostList
				}
			case input.Start:
				// abandon the edit
				hostText = ""
				if kbTarget == "host" {
					uiMode = uiHostList
				} else if kbTarget == "beast" {
					hostEditIdx = -1
					uiMode = uiBeastList
				} else if kbTarget == "ais" {
					hostEditIdx = -1
					uiMode = uiAISList
				} else {
					uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuFT8)
				}
			}
		case uiFreqEdit:
			switch b {
			case input.Left:
				if editCursor > 0 {
					editCursor--
				}
			case input.Right:
				if editCursor < 8 {
					editCursor++
				}
			case input.Up, input.Down:
				d := 1
				if b == input.Down {
					d = 9 // -1 mod 10
				}
				bb := []byte(editDigits)
				c := int(bb[editCursor]-'0') + d
				c %= 10
				bb[editCursor] = byte('0' + c)
				editDigits = string(bb)
			case input.A:
				commitFreq(editDigits)
				uiMode = uiMenu
			case input.B:
				uiMode = uiMenu
			}
		}
	}

	deadline := time.Time{}
	if *screenshot > 0 {
		deadline = time.Now().Add(time.Duration(*screenshot * float64(time.Second)))
	}

	diagOn := cfg["diag"] != "off" // default ON for remote debugging
	var lastDiagUpload time.Time

	sysinfo.Start()

	tick := time.NewTicker(33 * time.Millisecond)
	defer tick.Stop()

	// Watchdog: if the frame counter stops advancing, dump every
	// goroutine's stack to the log — the next launch log then shows
	// exactly where the app is stuck.
	var frames uint64
	go func() {
		var last uint64
		buf := make([]byte, 1<<20)
		for range time.Tick(15 * time.Second) {
			cur := atomic.LoadUint64(&frames)
			if cur == last {
				n := runtime.Stack(buf, true)
				fmt.Fprintf(os.Stderr,
					"WATCHDOG: frame counter stuck at %d — full goroutine dump:\n%s\n", cur, buf[:n])
			}
			last = cur
		}
	}()
	lastBeat := time.Now()
	lastSave := time.Now()

	for {
	drain:
		for {
			select {
			case f := <-webTasks:
				f()
			default:
				break drain
			}
		}
		// GPS follow: the radar, distances and the web map center on the
		// live GPS fix (a fix older than 10 s is ignored so a lost signal
		// doesn't drag the receiver marker).
		if gpsFollow {
			if f := gpsRx.Snapshot(); f.Valid && time.Since(f.Updated) < 10*time.Second {
				adsbLat, adsbLon = f.Lat, f.Lon
			}
		}
		// APRS: drain decoded frames into the store, then the beacon
		// scheduler (interval or SmartBeaconing) queues AFSK audio.
		for _, f := range r.APRSDemod().TakeFrames() {
			aprsStore.ProcessFrame(f)
		}
		if aprsBeaconIdx > 0 && !r.BeaconPlaying() {
			f, have := beaconFix(aprsSrc, gpsRx, aprsFixLat, aprsFixLon)
			due := false
			if have {
				iv := []int{0, 60, 120, 300, 600, 1800}[aprsBeaconIdx]
				if aprsBeaconIdx == 6 {
					iv = aprs.BeaconInterval(f.SpeedKt, 60, 1800)
					if aprs.TurnBeaconDue(f.CourseDeg, f.SpeedKt, aprsLastCourse,
						time.Since(aprsLastBeacon), 20) {
						due = true
					}
				}
				if !due && iv > 0 && time.Since(aprsLastBeacon) > time.Duration(iv)*time.Second {
					due = true
				}
			}
			if due && aprsCall != "" {
				aprsLastBeacon = time.Now()
				aprsLastCourse = f.CourseDeg
				if body, info, ok := buildAPRSBeacon(f, aprsCall, aprsPath, aprsSymList[aprsSymIdx], aprsCmt); ok {
					r.PlayBeacon(aprs.Modulate(body, float64(aprsLvl)/100*0.9, aprsPreambleFlags(aprsPre)))
					setMsg(i18n.T("aprs_sent"))
					aprsStore.SetOwn(aprs.Station{Call: aprsCall, Lat: f.Lat, Lon: f.Lon,
						Table: aprsSymList[aprsSymIdx].table, Sym: aprsSymList[aprsSymIdx].sym,
						SpeedKt: f.SpeedKt, CourseDeg: f.CourseDeg, HasCS: f.SpeedKt >= 1,
						AltFt: int(f.Alt * 3.28084), HasAlt: f.Alt != 0, Comment: aprsCmt})
					aprsStore.LogTX(aprs.LogEntry{At: time.Now(), Call: aprsCall, Lat: f.Lat, Lon: f.Lon,
						SpeedKt: f.SpeedKt, AltFt: int(f.Alt * 3.28084), Comment: aprsCmt, Info: info, Via: "RF", Sym: string(aprsSymList[aprsSymIdx].sym)})
					if aprsISOn {
						aprsStore.LogTX(aprs.LogEntry{At: time.Now(), Call: aprsCall, Lat: f.Lat, Lon: f.Lon,
							SpeedKt: f.SpeedKt, AltFt: int(f.Alt * 3.28084), Comment: aprsCmt, Info: info, Via: "IS", Sym: string(aprsSymList[aprsSymIdx].sym)})
					}
					postAPRSIS(aprsISOn, aprsIServer, aprsCall, aprsPath, info)
				}
			}
		}
		// GPS clock sync: the console has no RTC battery and its clock
		// drifts across power-offs (it even breaks TLS validity windows
		// for OTA). With a valid fix, set the system clock from the
		// satellites when it has drifted >30 s; re-check hourly.
		if gpsTimeSync && time.Since(gpsLastClockCheck) > time.Hour {
			gpsLastClockCheck = time.Now()
			if f := gpsRx.Snapshot(); f.Valid && len(f.TimeUTC) >= 6 && len(f.DateUTC) >= 6 {
				if t, err := time.Parse("020106150405", f.DateUTC+f.TimeUTC[:6]); err == nil {
					if drift := time.Since(t); drift > 30*time.Second || drift < -30*time.Second {
						if out, err := exec.Command("date", "-u", "-s", t.UTC().Format("2006-01-02 15:04:05 UTC")).CombinedOutput(); err == nil {
							fmt.Fprintf(os.Stderr, "gps: clock synced (was off by %v)\n", drift)
							setMsg(fmt.Sprintf(i18n.T("gps_clock_set"), drift.Round(time.Second)))
						} else {
							fmt.Fprintf(os.Stderr, "gps: clock set failed: %v: %s\n", err, out)
						}
					}
				}
			}
		}
		select {
		case <-exitMenu:
			quit()
			return
		default:
		}
		if ctx.Err() != nil {
			return
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}

		if diagOn && time.Since(lastDiagUpload) >= 60*time.Second {
			lastDiagUpload = time.Now()
			go func() {
				logPath := filepath.Join(filepath.Dir(mustExe()), "..", "SDRg35xx-logfile.txt")
				data, err := os.ReadFile(logPath)
				if err != nil {
					return
				}
				resp, err := http.Post("https://downloads.catgg.net/sdrg35xx/upload.php", "text/plain", bytes.NewReader(data))
				if err != nil {
					fmt.Fprintf(os.Stderr, "diag upload: %v\n", err)
					return
				}
				resp.Body.Close()
				fmt.Fprintf(os.Stderr, "diag upload: %d bytes sent\n", len(data))
			}()
		}
		r.FT8Process()

		if pad != nil {
			pad.Poll()
			for _, ev := range pad.Events() {
				held[ev.Button] = ev.Down
				switch ev.Button {
				case input.Power:
					if ev.Down {
						panelState = backlight.Cycle(panelState)
						fmt.Fprintf(os.Stderr, "power: panel state %d (%s)\n", panelState,
							[]string{"on", "dim", "off"}[panelState])
					}
					continue
				case input.Menu:
					if ev.Down {
						menuDownAt = time.Now()
					} else {
						if !menuInCombo && time.Since(menuDownAt) < 3*time.Second {
							// Short tap: toggle the settings menu
							// (or back out of the freq editor).
							switch uiMode {
							case uiFreqEdit, uiMenu, uiFT8Log, uiHostEdit, uiHostList, uiSysMon, uiLogs, uiBmList, uiMap:
								uiMode = uiMain
							default:
								uiMode = uiMenu
							}
						}
						menuInCombo = false
						menuDownAt = time.Time{}
					}
					continue
				case input.Start:
					if ev.Down {
						startDownAt = time.Now()
					} else {
						startDownAt = time.Time{}
					}
					continue
				}
				if ev.Down {
					// Volume: the initial tap is an instant 0.1% step;
					// the hold acceleration lives in the repeat loop below.
					switch ev.Button {
					case input.VolDown, input.VolUp:
						if held[input.Menu] {
							// MENU+Vol = backlight, like the game
							// launcher. Each step restarts the MENU
							// hold-to-exit timer so adjusting (even
							// holding with repeat) can never quit.
							d := 16
							if ev.Button == input.VolDown {
								d = -16
							}
							panelState = 0
							brightComboUntil = time.Now().Add(400 * time.Millisecond)
							menuInCombo = true
							menuDownAt = time.Now()
							capturedMsg = fmt.Sprintf("☀ %d", backlight.Step(d))
							capturedAt = time.Now()
						} else if ev.Button == input.VolDown {
							v := r.Volume() - 0.001
							if v < 0 {
								v = 0
							}
							r.SetVolume(v)
						} else {
							v := r.Volume() + 0.001
							if v > 1.5 {
								v = 1.5
							}
							r.SetVolume(v)
						}
					case input.L1, input.R1:
						if held[input.Menu] && r.WefaxEnabled() {
							// MENU+L1/R1 = nudge the WEFAX raster line
							// start (the decoder's Shift existed but was
							// never wired to any control).
							d := 5
							if ev.Button == input.L1 {
								d = -5
							}
							r.Wefax().Shift(float64(d) / 100)
							menuInCombo = true
							brightComboUntil = time.Now().Add(400 * time.Millisecond)
							menuDownAt = time.Now()
							capturedMsg = fmt.Sprintf(i18n.T("wfx_shift"), d)
							capturedAt = time.Now()
						} else {
							handlePress(ev.Button)
						}
					default:
						handlePress(ev.Button)
					}
					lastRepeat[ev.Button] = time.Now()
				}
			}
			// MENU+START together = screenshot from ANY screen (the
			// frame is already composed everywhere — menu, keyboard, FT8
			// log, map, sysmon). Fires once per joint press; the exit
			// timers restart at the shot so a quick press captures and
			// only a further 3 s hold exits.
			if held[input.Menu] && held[input.Start] {
				if !shotCombo {
					shotCombo = true
					menuInCombo = true
					menuDownAt, startDownAt = time.Now(), time.Now()
					capture()
				}
			} else {
				shotCombo = false
			}
			// Hold-to-exit: MENU or START held 3 s — but never while a
			// MENU+Vol brightness combo is running (its steps keep
			// restarting the timer; the gate also hides the countdown
			// hint while adjusting).
			exitHint = ""
			if time.Now().Before(brightComboUntil) {
				// brightness combo active: skip the exit check entirely
			} else if !menuDownAt.IsZero() || !startDownAt.IsZero() {
				var d time.Duration
				if !menuDownAt.IsZero() {
					d = time.Since(menuDownAt)
				} else {
					d = time.Since(startDownAt)
				}
				if d >= 3*time.Second {
					quit()
				} else {
					exitHint = fmt.Sprintf(i18n.T("hold_exit"), float64(3*time.Second-d)/float64(time.Second))
				}
			}
			// Volume keys work EVERYWHERE (main screen, menus, dialogs)
			// — instantaneous tap = 0.1%; held = exponential ramp. With
			// MENU held the same keys repeat backlight steps instead
			// (fixed ±16, and every step restarts the exit timer).
			for _, b := range []input.Button{input.VolDown, input.VolUp} {
				if held[b] && time.Since(lastRepeat[b]) > (func() time.Duration {
					if volHeldAt[b].IsZero() {
						return 450 * time.Millisecond
					}
					return 150 * time.Millisecond
				})() {
					if held[input.Menu] {
						if volHeldAt[b].IsZero() {
							volHeldAt[b] = time.Now()
						}
						d := 16
						if b == input.VolDown {
							d = -16
						}
						panelState = 0
						brightComboUntil = time.Now().Add(400 * time.Millisecond)
						menuInCombo = true
						menuDownAt = time.Now()
						capturedMsg = fmt.Sprintf("☀ %d", backlight.Step(d))
						capturedAt = time.Now()
						lastRepeat[b] = time.Now()
						continue
					}
					if volHeldAt[b].IsZero() {
						volHeldAt[b] = time.Now()
					}
					holdSec := time.Since(volHeldAt[b]).Seconds()
					mult := 1.0 + holdSec*holdSec*0.5
					if mult > 50 {
						mult = 50
					}
					step := 0.001 * mult
					dir := 1.0
					if b == input.VolDown {
						dir = -1
					}
					v := r.Volume() + dir*step
					if v < 0 {
						v = 0
					}
					if v > 1.5 {
						v = 1.5
					}
					r.SetVolume(v)
					lastRepeat[b] = time.Now()
				}
				if !held[b] && !volHeldAt[b].IsZero() {
					delete(volHeldAt, b)
					lastRepeat[b] = time.Time{}
				}
			}
			// Key repeat for held tuning buttons (main screen only):
			// 450 ms delay, then every 150 ms.
			if uiMode == uiMain {
				for _, b := range []input.Button{input.Left, input.Right, input.Up, input.Down} {
					if held[b] && time.Since(lastRepeat[b]) > 450*time.Millisecond {
						act(b)
						lastRepeat[b] = lastRepeat[b].Add(150 * time.Millisecond)
						if time.Since(lastRepeat[b]) < 0 {
							lastRepeat[b] = time.Now()
						}
					}
				}
			}
		}

		// FT8 slot boundary: mark the newest waterfall row red once per
		// 15 s slot after a Y sync (pending until a fresh row arrives).
		if r.FT8Enabled() && !ft8SyncWall.IsZero() {
			if idx := int(time.Since(ft8SyncWall) / (15 * time.Second)); idx > ft8SlotIdx {
				ft8SlotIdx = idx
				ft8SlotMark = true
			}
		}
		// Full-screen views (radar, FT8 world map) paint over the
		// waterfall entirely — skipping the spectrum FFT + waterfall
		// scroll there saves the biggest UI-side cost while every
		// decoder (FT8/RTTY/AIS) keeps feeding from the DSP chain.
		if uiMode != uiADSB && uiMode != uiMap {
			if newRow := u.NewSpectrumRow(r.Tap(), r.RawTap()); newRow && ft8SlotMark {
				u.MarkFT8Slot(time.Now().Format("2006-01-02 15:04:05"))
				ft8SlotMark = false
			}
		}
		snap := r.Snapshot()
		status := snap.StatusText
		if r.FT8Enabled() && !r.FT8Synced() {
			status = i18n.T("ft8_need_sync")
		}
		if ft8s := r.FT8Results(); len(ft8s) > 0 {
			best := ft8s[0]
			for _, d := range ft8s[1:] {
				if d.SNRDb > best.SNRDb {
					best = d
				}
			}
			if best.Message != nil && best.Message.Valid {
				status = "FT8: " + best.Message.Text
			} else {
				status = fmt.Sprintf("FT8: %.0f Hz %.0f dB (%.0f%%)", best.FreqHz, best.SNRDb, best.Confidence*100)
			}
		}
		if exitHint != "" {
			status = exitHint
		}
		if capturedMsg != "" && time.Since(capturedAt) < 3*time.Second {
			status = capturedMsg
		}
		if m := upd.Msg(); m != "" {
			status = m
		}
		// Drain decoded messages every frame. A transmission sits in
		// the 15 s waterfall for ~2.2 s and scans run several times per
		// second, so the same text decodes over and over — and with two
		// stations alternating, the old last-entry check leaked
		// duplicates (one message logged 23×). Dedup by a 6 s window
		// instead: covers the ring overlap, but a genuine repeat in the
		// next 15 s slot still shows.
		for _, m := range r.FT8TakeMessages() {
			if !m.Valid {
				continue
			}
			now := time.Now()
			dup := false
			for _, s := range recentFT8 {
				if s.text == m.Text && now.Sub(s.at) < 6*time.Second {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			recentFT8 = append(recentFT8, ft8Seen{text: m.Text, at: now})
			if len(recentFT8) > 40 {
				recentFT8 = recentFT8[len(recentFT8)-40:]
			}
			// Annotate with country (from callsign prefix) and distance
			// (from our grid to theirs). The SENDER is the SECOND token.
			// Grids are cached per-callsign: report/RRR/73 messages
			// don't carry a grid, but we can reuse one seen earlier.
			anno := ""
			if toks := strings.Fields(m.Text); len(toks) >= 2 {
				call := toks[1]
				grid := toks[len(toks)-1]
				isTail := grid == "RR73" || grid == "RRR" || grid == "73" || grid == "CQ"
				hasGrid := !isTail && len(grid) >= 4 && grid[0] >= 'A' && grid[0] <= 'R' &&
					grid[1] >= 'A' && grid[1] <= 'R' &&
					grid[2] >= '0' && grid[2] <= '9' && grid[3] >= '0' && grid[3] <= '9'
				if hasGrid {
					gridCache[call] = grid
				} else if g, ok := gridCache[call]; ok {
					grid = g
					hasGrid = true
				}
				var country string
				if hasGrid && myGrid != "" {
					country = geo.Country(call)
					dist := geo.DistanceKm(myGrid, grid)
					if country != "" && dist > 0 {
						anno = fmt.Sprintf("%s %dkm", country, dist)
					} else if country != "" {
						anno = country
					} else if dist > 0 {
						anno = fmt.Sprintf("%dkm", dist)
					}
				} else {
					country = geo.Country(call)
					if country != "" {
						anno = country
					}
				}
			}
			isoCC := ""
			if toks := strings.Fields(m.Text); len(toks) >= 2 {
				isoCC = geo.CountryISO(toks[1])
			}
			ft8Log = append(ft8Log, ui.FT8Entry{Time: now.Format("15:04:05"), SNRDb: m.SNRDb, FreqHz: m.FreqHz, Text: m.Text, Anno: anno, FlagCC: isoCC})
			webSrv.AddFT8(web.FT8Line{Time: now.Format("15:04:05"), SNR: m.SNRDb, Hz: m.FreqHz, Text: m.Text, Anno: anno})
			if len(ft8Log) > 100 {
				ft8Log = ft8Log[len(ft8Log)-100:]
			}
			// Report to PSK Reporter: the SENDER is the SECOND token
			// ("RECIPIENT SENDER grid/report" — the transmitter's call
			// comes after the addressee; see essexham.co.uk). For CQ
			// messages the sender follows the CQ token (also position
			// 2). Hash/telemetry texts are skipped.
			if toks := strings.Fields(m.Text); len(toks) >= 2 {
				sender := toks[1]
				if toks[0] == "CQ" || strings.HasPrefix(toks[0], "CQ_") {
					// CQ [DX/NA/EU/…] SENDER grid
					if sender == "DX" || sender == "NA" || sender == "EU" || sender == "AS" ||
						sender == "JA" || sender == "OC" || sender == "SA" || sender == "AF" ||
						sender == "RU" || sender == "AN" || sender == "FD" || sender == "TEST" ||
						sender == "FIELD" || sender == "POTA" || sender == "SOTA" || sender == "RR73" {
						if len(toks) >= 3 {
							sender = toks[2]
						} else {
							sender = ""
						}
					}
				}
				if isSpotCallsign(sender) {
					// SNR in the standard 2500 Hz convention —
					// typically negative for FT8; keep within the
					// int8 field's sane range.
					sn := int8(m.SNRDb)
					if sn < -40 {
						sn = -40
					}
					if sn > 40 {
						sn = 40
					}
					psk.Add(pskreporter.Spot{
						Sender: sender,
						FreqHz: uint32(float64(r.Freq()) + m.FreqHz),
						SNRDb:  sn,
						At:     now,
					})
				}
			}
		}
		// Power-loss guard: flush the ini every 2 minutes on every
		// path (not only screen-off) so a hard power-off mid-session
		// keeps recent settings such as freshly added server lists.
		if time.Since(lastSave) >= 2*time.Minute {
			saveNow()
			lastSave = time.Now()
		}
		if panelState == 2 {
			// Screen off: skip the ENTIRE render pipeline — spectrum
			// FFT, waterfall scroll, overlay drawing and sysinfo
			// sampling all burn CPU for pixels nobody sees. Input
			// polling, FT8 decode + PSK Reporter spotting, the audio
			// DSP and the heartbeat keep running. The extra sleep
			// stretches the loop from ~30 Hz to ~10 Hz; the power key
			// still wakes within ~0.1 s.
			time.Sleep(70 * time.Millisecond)
			frames++
			// Power-loss guard: flush the ini every 2 minutes so a hard
			// power-off mid-session keeps the recent settings (quit and the
			// update path both save, but a pulled battery does not).
			if time.Since(lastSave) >= 2*time.Minute {
				saveNow()
				lastSave = time.Now()
			}
			if time.Since(lastBeat) >= 10*time.Second {
				s := r.Snapshot()
				var af, astall int64
				if out != nil {
					af, astall = out.Stats()
				}
				fmt.Fprintf(os.Stderr, "alive(screen-off): frames=%d connected=%v freq=%.4f MHz mode=%s bytes=%d audioFrames=%d maxStall=%dms\n",
					atomic.LoadUint64(&frames), s.Connected, float64(r.Freq())/1e6, r.Mode().Name, s.BytesRx, af, astall)
				lastBeat = time.Now()
			}
			continue
		}
		cpu, mem, swp := sysinfo.Snapshot()
		// Passband tuning: pan the view so the listening bracket stays
		// on screen — the view centre trails the listening offset,
		// clamped so we never show beyond the real spectrum.
		loHz := r.LO()
		listenOff := float64(r.Freq() - loHz)
		viewOff := u.ViewOffHzSmooth(listenOff)
		u.SetViewOff(viewOff)
		pbLo, pbHi := r.PassbandHz()
		frame := u.Frame(ui.FrameStats{
			FreqHz:      r.Freq(),
			Mode:        r.Mode().Name,
			StepHz:      stepFor(),
			Connected:   snap.Connected,
			StatusText:  status,
			PowerDb:     snap.PowerDb,
			Squelch:     r.Mode().Squelch,
			SquelchOpen: snap.SquelchOpen,
			Volume:      r.Volume(),
			GainText:    r.SquelchLabel(),
			Host:        r.Hostname(),
			LOHz:        loHz,
			BwHz:        r.Bandwidth(),
			PbLo:        pbLo,
			PbHi:        pbHi,
			AmMode:      r.Mode().Name == "AM",
			CpuPct:      cpu,
			MemPct:      mem,
			SwpPct:      swp,
		})
		// Frequency tick ruler on the waterfall (main screen only —
		// the menu/FT8 windows cover it anyway).
		if uiMode == uiMain {
			u.DrawFreqScale(loHz + int64(viewOff))
		}
		// Settings overlays on top of the composed frame.
		if uiMode == uiMenu {
			sq := r.SquelchLabel()
			if v := r.SquelchDb(); v >= 40 {
				sq = i18n.T("sql_off")
			} else {
				sq = fmt.Sprintf("%.0f dB", v)
			}
			items := []ui.MenuItem{}
			pskVal := i18n.T("off")
			if pskOn {
				pskVal = i18n.T("on")
			}
			switch menuPage {
			case pageRoot:
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_rxpage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_audiopage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_disppage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_adsbpage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_gpspage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_aprspage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_ft8page"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_stationpage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_bm"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_syspage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_exit"), Value: i18n.T("press_a")})
			case pageRx:
				freqDec := 5
				switch r.Mode().Name {
				case "WFM", "AM":
					freqDec = 3
				case "NFM", "USB", "LSB", "CW":
					freqDec = 4
				}
				items = append(items,
					func() ui.MenuItem {
						v := r.Hostname()
						if *host == "" {
							v = "(" + i18n.T("off") + ")"
						}
						return ui.MenuItem{Label: i18n.T("m_host"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("m_rate"), Value: fmt.Sprintf("%.3fM", float64(r.IQRate())/1e6)},
					ui.MenuItem{Label: i18n.T("m_freq"), Value: fmt.Sprintf("%.*f MHz >", freqDec, float64(r.Freq())/1e6)},
					ui.MenuItem{Label: i18n.T("m_step"), Value: stepLabel(stepHz)},
					func() ui.MenuItem {
						if r.PpmOff() {
							return ui.MenuItem{Label: i18n.T("m_ppm"), Value: i18n.T("off")}
						}
						return ui.MenuItem{Label: i18n.T("m_ppm"), Value: fmt.Sprintf("%+d ppm", r.Ppm())}
					}(),
					func() ui.MenuItem {
						m := ui.MenuItem{Label: i18n.T("m_mode"), Value: r.Mode().Name}
						if r.FT8Enabled() {
							m.Value = "USB (FT8)"
						}
						return m
					}(),
					ui.MenuItem{Label: i18n.T("m_gain"), Value: fmt.Sprintf("%.1f dB", r.GainDb())},
					ui.MenuItem{Label: i18n.T("m_sql"), Value: sq},
					ui.MenuItem{Label: i18n.T("m_bw"), Value: bwLabel(r.Bandwidth())},
					ui.MenuItem{Label: i18n.T("m_ds"), Value: r.DirectSamplingLabel()},
					ui.MenuItem{Label: i18n.T("m_agc"), Value: agcLabel(r.AGCEnabled())})
			case pageDisp:
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_span"), Value: fmt.Sprintf("%d kHz", u.SpanFull/1000)},
					ui.MenuItem{Label: i18n.T("m_wfmin"), Value: fmt.Sprintf("+%.0f dB", wfMin)},
					ui.MenuItem{Label: i18n.T("m_wfmax"), Value: fmt.Sprintf("%.0f dB", wfMax)})
			case pageFT8:
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_map"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_ft8"), Value: ft8Label(r.FT8Enabled())},
					ui.MenuItem{Label: i18n.T("m_bands"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_rtty"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.RTTYEnabled()]},
					ui.MenuItem{Label: i18n.T("m_rttylog"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_wefax"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.WefaxEnabled()]},
					ui.MenuItem{Label: i18n.T("m_wefaxauto"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.WefaxAutoSave()]},
					ui.MenuItem{Label: i18n.T("m_wefaxclear"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_cwdec"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.CWDecodeEnabled()]},
					ui.MenuItem{Label: i18n.T("m_cwclear"), Value: i18n.T("press_a")},
					func() ui.MenuItem {
						if !r.DeepCWAvailable() {
							return ui.MenuItem{Label: i18n.T("m_deepcw"), Value: i18n.T("deepcw_missing")}
						}
						return ui.MenuItem{Label: i18n.T("m_deepcw"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.DeepCWEnabled()]}
					}(),
					ui.MenuItem{Label: i18n.T("m_deepcwth"), Value: func() string {
						t, _ := r.DeepCWParams()
						if t < 1 {
							t = 1
						}
						return fmt.Sprintf("%d", t)
					}()},
					ui.MenuItem{Label: i18n.T("m_deepcwwin"), Value: func() string {
						_, w := r.DeepCWParams()
						if w < 3 {
							w = 5
						}
						return fmt.Sprintf("%d s", w)
					}()},
					ui.MenuItem{Label: i18n.T("m_deepcwclear"), Value: i18n.T("press_a")},
					func() ui.MenuItem {
						if !r.FT8TSAvailable() {
							return ui.MenuItem{Label: i18n.T("m_ft8ts"), Value: i18n.T("ft8ts_missing")}
						}
						return ui.MenuItem{Label: i18n.T("m_ft8ts"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.FT8TSEnabled()]}
					}(),
					ui.MenuItem{Label: i18n.T("m_ft8tsdepth"), Value: func() string {
						d, _, _, _ := r.FT8TSParams()
						if d < 1 {
							d = 2
						}
						return fmt.Sprintf("%d", d)
					}()},
					ui.MenuItem{Label: i18n.T("m_ft8tsth"), Value: func() string {
						_, t, _, _ := r.FT8TSParams()
						if t < 1 {
							t = 1
						}
						return fmt.Sprintf("%d", t)
					}()},
					ui.MenuItem{Label: i18n.T("m_ft8tsband"), Value: func() string {
						_, _, lo, hi := r.FT8TSParams()
						if lo < 50 {
							lo = 200
						}
						if hi < 500 {
							hi = 3000
						}
						return fmt.Sprintf("%d-%d Hz", lo, hi)
					}()},
					ui.MenuItem{Label: i18n.T("m_sstv"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.SSTVEnabled()]},
					ui.MenuItem{Label: i18n.T("m_sstvview"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_sstvclear"), Value: i18n.T("press_a")})
			case pageStation:
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_call"), Value: myCall},
					ui.MenuItem{Label: i18n.T("m_grid"), Value: myGrid},
					ui.MenuItem{Label: i18n.T("m_ant"), Value: myAnt},
					ui.MenuItem{Label: i18n.T("m_rig"), Value: myRig},
					ui.MenuItem{Label: i18n.T("m_psk"), Value: pskVal})
			case pageAudio:
				nrVal := i18n.T("off")
				if lv := r.NoiseReduction(); lv > 0 {
					nrVal = fmt.Sprintf("%d", lv)
				}
				hp, lp := r.AudioFilter()
				hpVal, lpVal := i18n.T("off"), i18n.T("off")
				if hp > 0 {
					hpVal = fmt.Sprintf("%d Hz", hp)
				}
				if lp > 0 {
					lpVal = fmt.Sprintf("%d Hz", lp)
				}
				afVal := i18n.T("af_custom")
				if i := audioFilterPresetIndex(hp, lp); i >= 0 {
					afVal = i18n.T(audioFilterPresets[i].i18nKey)
				}
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_af"), Value: afVal},
					ui.MenuItem{Label: i18n.T("m_nr"), Value: nrVal},
					ui.MenuItem{Label: i18n.T("m_hp"), Value: hpVal},
					ui.MenuItem{Label: i18n.T("m_lp"), Value: lpVal},
					func() ui.MenuItem {
						on, mode := r.NREnabled()
						v := i18n.T("off")
						if on {
							v = map[string]string{"voice": i18n.T("nr_voice"), "cw": i18n.T("nr_cw")}[mode]
						} else if !r.NRAvailable() {
							v = i18n.T("nr_missing")
						}
						return ui.MenuItem{Label: i18n.T("m_nrnn"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("m_lmute"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.LocalMuted()]},
					ui.MenuItem{Label: i18n.T("m_vol"), Value: fmt.Sprintf("%.1f%%", r.Volume()*100)})
			case pageADSB:
				// Row order MUST mirror pageItems[pageADSB] exactly —
				// activateItem dispatches by row index against that
				// list; a drift here lands presses on the neighbouring
				// row action.
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_adsbradar"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_adsblat"), Value: fmt.Sprintf("%.5f", adsbLat)},
					ui.MenuItem{Label: i18n.T("m_adsblon"), Value: fmt.Sprintf("%.5f", adsbLon)},
					ui.MenuItem{Label: i18n.T("m_adsbrf"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.ADSBRFEnabled()]},
					ui.MenuItem{Label: i18n.T("m_rtlsrv"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.RTLSrvEnabled()]},
					ui.MenuItem{Label: i18n.T("m_rtlsrvport"), Value: func() string {
						p := r.RTLSrvPort()
						if p == 0 {
							p = rtlSrvPort
						}
						return fmt.Sprintf("%d", p)
					}()},
					func() ui.MenuItem {
						v := adsbHost
						if v == "" {
							v = "(" + i18n.T("off") + ")"
						}
						return ui.MenuItem{Label: i18n.T("m_adsbhost"), Value: v}
					}(),
					func() ui.MenuItem {
						v := aisHost
						if v == "" {
							v = "(" + i18n.T("off") + ")"
						}
						return ui.MenuItem{Label: i18n.T("m_aishost"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("m_aisrf"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.AISRFEnabled()]},
					ui.MenuItem{Label: i18n.T("m_aislog"), Value: i18n.T("press_a")},
					func() ui.MenuItem {
						v := i18n.T("press_a")
						if time.Since(mapCacheClearedAt) < 10*time.Second {
							v = i18n.T("cache_cleared")
						}
						return ui.MenuItem{Label: i18n.T("m_clearcache"), Value: v}
					}())
			case pageAPRS:
				beaconNames := []string{i18n.T("off"), "1", "2", "5", "10", "30", "smart"}
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_aprsrx"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[r.APRSEnabled()]},
					ui.MenuItem{Label: i18n.T("m_aprsfreq"), Value: fmt.Sprintf("%.4f MHz >", float64(aprsFreq)/1e6)},
					ui.MenuItem{Label: i18n.T("m_aprscall"), Value: aprsCall + " >"},
					func() ui.MenuItem {
						v := beaconNames[aprsBeaconIdx]
						if aprsBeaconIdx >= 1 && aprsBeaconIdx <= 5 {
							v = fmt.Sprintf(i18n.T("aprs_every_min"), v)
						}
						return ui.MenuItem{Label: i18n.T("m_aprsbeacon"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("m_aprsis"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[aprsISOn]},
					ui.MenuItem{Label: i18n.T("m_aprsisrv"), Value: aprsIServer + " >"},
					ui.MenuItem{Label: i18n.T("m_aprspath"), Value: aprsPath + " >"},
					ui.MenuItem{Label: i18n.T("m_aprssym"), Value: i18n.T(aprsSymList[aprsSymIdx].i18nKey)},
					ui.MenuItem{Label: i18n.T("m_aprscmt"), Value: aprsCmt + " >"},
					ui.MenuItem{Label: i18n.T("m_aprspre"), Value: fmt.Sprintf("%.1fs", aprsPre)},
					ui.MenuItem{Label: i18n.T("m_aprslvl"), Value: fmt.Sprintf("%d%%", aprsLvl)},
					ui.MenuItem{Label: i18n.T("m_aprsstat"), Value: fmt.Sprintf("%d", aprsStore.Count())},
					ui.MenuItem{Label: i18n.T("m_aprslog"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_aprsigate"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[aprsIgateOn]},
					ui.MenuItem{Label: i18n.T("m_aprsgatelimit"), Value: fmt.Sprintf("%d/min", aprsGateLim)},
					ui.MenuItem{Label: i18n.T("m_aprssrc"), Value: map[string]string{"gps": "GPS", "fix": "FIX"}[aprsSrc]},
					ui.MenuItem{Label: i18n.T("m_aprsfixlat"), Value: fmt.Sprintf("%.5f >", aprsFixLat)},
					ui.MenuItem{Label: i18n.T("m_aprsfixlon"), Value: fmt.Sprintf("%.5f >", aprsFixLon)},
					ui.MenuItem{Label: i18n.T("m_aprsnow"), Value: i18n.T("press_a")},
				)
			case pageGPS:
				gf := gpsRx.Snapshot()
				gd := gpsRx.Device()
				items = append(items,
					func() ui.MenuItem {
						v := i18n.T("gps_nofix")
						if gd != "" {
							v = gd
						}
						return ui.MenuItem{Label: i18n.T("gps_dev"), Value: v}
					}(),
					func() ui.MenuItem {
						v := i18n.T("gps_nofix")
						switch {
						case !gf.Valid:
							v = i18n.T("gps_nofix")
						case gf.Quality >= 2:
							v = i18n.T("gps_fix2")
						default:
							v = i18n.T("gps_fix1")
						}
						if gf.Valid && time.Since(gf.Updated) > 10*time.Second {
							v = i18n.T("gps_stale")
						}
						return ui.MenuItem{Label: i18n.T("gps_stat"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						if l := gps.TimeLabel(gf); l != "" {
							v = l
						}
						return ui.MenuItem{Label: i18n.T("gps_time"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						if gf.Lat != 0 || gf.Lon != 0 {
							v = fmt.Sprintf("%.5f, %.5f", gf.Lat, gf.Lon)
						}
						return ui.MenuItem{Label: i18n.T("gps_pos"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						// Maidenhead(0,0) is a real locator ("JJ00aa") — only
						// show one once a position exists.
						if gf.Lat != 0 || gf.Lon != 0 {
							v = gpsRx.Grid()
						}
						return ui.MenuItem{Label: i18n.T("gps_grid"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						if gf.Alt != 0 {
							v = fmt.Sprintf("%.1f m", gf.Alt)
						}
						return ui.MenuItem{Label: i18n.T("gps_alt"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						if gf.SpeedKt > 0.05 {
							v = fmt.Sprintf("%.1f kt / %.1f km/h", gf.SpeedKt, gf.SpeedKt*1.852)
						}
						return ui.MenuItem{Label: i18n.T("gps_spd"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						if gf.CourseDeg > 0.05 {
							v = fmt.Sprintf("%.0f°", gf.CourseDeg)
						}
						return ui.MenuItem{Label: i18n.T("gps_cse"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("gps_sats"), Value: fmt.Sprintf("%d / %d", gf.SatsUsed, gf.SatsView)},
					func() ui.MenuItem {
						v := "-"
						if gf.HDOP > 0 {
							v = fmt.Sprintf("%.1f", gf.HDOP)
						}
						return ui.MenuItem{Label: i18n.T("gps_hdop"), Value: v}
					}(),
					func() ui.MenuItem {
						v := "-"
						if !gf.Updated.IsZero() {
							v = fmt.Sprintf("%ds", int(time.Since(gf.Updated).Seconds()))
						}
						return ui.MenuItem{Label: i18n.T("gps_age"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("m_gpsfollow"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[gpsFollow]},
					ui.MenuItem{Label: i18n.T("m_gpstimesync"), Value: map[bool]string{true: i18n.T("on"), false: i18n.T("off")}[gpsTimeSync]},
				)
			case pageSys:
				items = append(items,
					func() ui.MenuItem {
						v := i18n.T("off")
						if webSrv.Enabled() {
							v = fmt.Sprintf("http://%s:%d", localIP(), webSrv.Port())
						}
						return ui.MenuItem{Label: i18n.T("m_web"), Value: v}
					}(),
					ui.MenuItem{Label: i18n.T("m_webport"), Value: fmt.Sprintf("%d", webSrv.Port())},
					ui.MenuItem{Label: i18n.T("m_lang"), Value: langLabel()},
					ui.MenuItem{Label: i18n.T("m_sysmon"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_logs"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_shot"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_update"), Value: i18n.T("press_a")})
			}
			u.DrawMenu(items, menuSel, fmt.Sprintf(i18n.T("menu_ver"), buildStamp, strings.ReplaceAll(buildTime, "_", " ")))
		} else if uiMode == uiFreqEdit {
			u.DrawFreqEditor(editDigits, editCursor)
		} else if uiMode == uiHostEdit {
			u.DrawKeyboard(kbTitle(), hostText, len(hostText), hostKbR, hostKbC, kbShifted)
		} else if uiMode == uiHostList {
			rows := append([]string{i18n.T("host_disable"), i18n.T("host_usb")}, hostList...)
			active := -1
			if *host == "" {
				active = 0
			} else if *host == "usb" {
				active = 1
			} else {
				for i, h := range hostList {
					if h == *host {
						active = i + 2
					}
				}
			}
			u.DrawHostList(rows, hostSel, active, i18n.T("host_title"))
		} else if uiMode == uiBeastList {
			rows := append([]string{i18n.T("host_disable")}, beastList...)
			active := -1
			if adsbHost == "" {
				active = 0
			} else {
				for i, h := range beastList {
					if h == adsbHost {
						active = i + 1
					}
				}
			}
			u.DrawHostList(rows, beastSel, active, i18n.T("beast_title"))
		} else if uiMode == uiAISList {
			rows := append([]string{i18n.T("host_disable")}, aisList...)
			active := -1
			if aisHost == "" {
				active = 0
			} else {
				for i, h := range aisList {
					if h == aisHost {
						active = i + 1
					}
				}
			}
			u.DrawHostList(rows, aisSel, active, i18n.T("ais_title"))
		} else if uiMode == uiBmList {
			labels := make([]string, len(bookmarks))
			active := -1
			for i, bm := range bookmarks {
				if bm.freqHz == r.Freq() && bm.mode == r.Mode().Name {
					active = i
				}
				mode := bm.mode
				if mode == "" {
					mode = "NFM"
				}
				if bm.label != "" {
					labels[i] = fmt.Sprintf("%s  %s  %.4f", bm.label, mode, float64(bm.freqHz)/1e6)
				} else {
					labels[i] = fmt.Sprintf("%s  %.4f MHz", mode, float64(bm.freqHz)/1e6)
				}
			}
			u.DrawBookmarkList(labels, bmSel, active, r.Mode().Name)
		} else if uiMode == uiFT8Log {
			u.DrawFT8LogFull(ft8Log, ft8Scroll, flagDir)
		} else if uiMode == uiAPRSLog {
			var stations []ui.APRSStationUI
			for _, st := range aprsStore.All() {
				stations = append(stations, ui.APRSStationUI{Call: st.Call, Country: geo.CountryISO(st.Call),
					Sym: string(st.Sym), Lat: st.Lat, Lon: st.Lon, SpeedKt: st.SpeedKt,
					AltFt: st.AltFt, Comment: st.Comment, AgeSec: time.Since(st.LastHeard).Seconds()})
			}
			mk := func(es []aprs.LogEntry) []ui.APRSLogUI {
				var out []ui.APRSLogUI
				for _, e := range es {
					extra := e.Comment
					if e.SpeedKt > 0.5 {
						extra = fmt.Sprintf("%.0fkt ", e.SpeedKt) + extra
					}
					out = append(out, ui.APRSLogUI{Time: e.At.Format("15:04:05"), Call: e.Call,
						Country: geo.CountryISO(e.Call), Sym: e.Sym, Pos: fmt.Sprintf("%.4f,%.4f", e.Lat, e.Lon),
						Extra: extra, Via: e.Via})
				}
				return out
			}
			u.DrawAPRSLog(aprsLogTab, stations, mk(aprsStore.RxLog()), mk(aprsStore.TxLog()), aprsLogScroll, flagDir)
		} else if uiMode == uiSSTV {
			img, name, line, total, ver := r.SSTV().Snapshot(sstvVer)
			if ver != sstvVer {
				sstvVer = ver
				sstvCache = img
			}
			if sstvCache != nil {
				img = sstvCache
			}
			u.DrawSSTV(img, name, line, total)
		} else if uiMode == uiAISLog {
			aisLogMu.Lock()
			view := make([]ui.AISEntry, len(aisLog))
			copy(view, aisLog)
			aisLogMu.Unlock()
			if aisScroll < 0 {
				aisScroll = 0
			}
			if aisScroll > len(view)-1 {
				aisScroll = len(view) - 1
			}
			if aisScroll < 0 {
				aisScroll = 0
			}
			u.DrawAISLogFull(view, aisScroll, flagDir)
		} else if uiMode == uiSysMon {
			sn := sysinfo.SensorSnapshot()
			cpu, mem, swp := sysinfo.Snapshot()
			memUsed, memTot := sysinfo.MemAbsolute()
			fDeg := func(v float64) string {
				if v == 0 {
					return "—"
				}
				return fmt.Sprintf("%.1f °C", v)
			}
			swapStr := "—"
			if swp > 0 {
				swapStr = fmt.Sprintf("%.0f %%", swp)
			}
			rows := []string{
				"# " + i18n.T("m_sysmon"),
				i18n.T("sm_cpu_temp") + "\t" + fDeg(sn.CPUTemp),
				i18n.T("sm_gpu_temp") + "\t" + fDeg(sn.GPUTemp),
				i18n.T("sm_ve_temp") + "\t" + fDeg(sn.VETemp),
				i18n.T("sm_ddr_temp") + "\t" + fDeg(sn.DDRTemp),
				i18n.T("sm_batt_temp") + "\t" + fDeg(sn.BattTemp),
				i18n.T("sm_batt_lvl") + "\t" + fmt.Sprintf("%d %%", sn.BattPct) + func() string {
					if strings.Contains(sn.BattStatus, "harg") {
						return " ⚡"
					}
					return ""
				}(),
				i18n.T("sm_batt_v") + "\t" + fmt.Sprintf("%.2f V", sn.BattVolt),
				i18n.T("sm_batt_st") + "\t" + sn.BattStatus,
				i18n.T("sm_cpu_use") + "\t" + fmt.Sprintf("%.0f %%", cpu),
				i18n.T("sm_mem_use") + "\t" + fmt.Sprintf("%.0f %%  ·  %.2f/%.2f GB", mem, memUsed/1024, memTot/1024),
				i18n.T("sm_swap_use") + "\t" + swapStr,
			}
			for i := range rows {
				if k, v, ok := strings.Cut(rows[i], "\t"); ok && v == "" {
					rows[i] = k + "\t—"
				}
			}
			u.DrawSysMon(rows)
		} else if uiMode == uiLogs {
			// Re-read the log file every 2 seconds while the viewer
			// is open (cheap: one ReadFile of a few hundred KB).
			if time.Since(logReadAt) >= 2*time.Second {
				logReadAt = time.Now()
				if data, err := os.ReadFile(filepath.Join(filepath.Dir(mustExe()), "..", "SDRg35xx-logfile.txt")); err == nil {
					lines := strings.Split(string(data), "\n")
					if len(lines) > 300 {
						lines = lines[len(lines)-300:]
					}
					logLines = lines
				}
			}
			rows := []string{"# Log"}
			vis := (u.WaterfallRows - 80) / 16
			if vis < 1 {
				vis = 1
			}
			start := len(logLines) - vis - logScroll
			if start < 0 {
				start = 0
			}
			for i := start; i < start+vis && i < len(logLines); i++ {
				ln := logLines[i]
				if len(ln) > 80 {
					ln = ln[:80]
				}
				rows = append(rows, ln)
			}
			rows = append(rows, fmt.Sprintf("%d–%d / %d", start+1, start+vis, len(logLines)))
			u.DrawSysMon(rows)
		} else if uiMode == uiMap {
			// Build map entries and the station index from the last
			// 10 minutes of FT8 log. The sender (toks[1]) draws red,
			// the recipient (toks[0]) green; positions resolve to the
			// exact grid when known (message tail or cache), else the
			// country centroid — a coarse placeholder that upgrades
			// to the real grid as soon as that station is heard with
			// one.
			now := time.Now()
			mapEntries := []ui.MapEntry{}
			type mapStation struct {
				grid string
				msgs []string
			}
			stations := map[string]*mapStation{}
			station := func(call string) *mapStation {
				s := stations[call]
				if s == nil {
					s = &mapStation{}
					stations[call] = s
				}
				return s
			}
			stationPos := func(call, grid string) (lat, lon float64, approx, ok bool) {
				if grid != "" {
					lat, lon, ok = geo.GridToLatLon(grid)
					return lat, lon, false, ok
				}
				lat, lon, ok = geo.CountryLatLon(call)
				return lat, lon, true, ok
			}
			for _, e := range ft8Log {
				t, err := time.Parse("15:04:05", e.Time)
				if err != nil {
					continue
				}
				eTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), t.Second(), 0, now.Location())
				if eTime.After(now.Add(time.Hour)) {
					eTime = eTime.Add(-24 * time.Hour)
				}
				age := now.Sub(eTime)
				if age < 0 || age > 10*time.Minute {
					continue
				}
				toks := strings.Fields(e.Text)
				if len(toks) < 2 {
					continue
				}
				isCQ := toks[0] == "CQ" || strings.HasPrefix(toks[0], "CQ_")
				// The message grid (tail token) belongs to the SENDER
				// (toks[1]); report/RRR/73 tails carry none and fall
				// back to the sender's cached grid.
				lastTok := toks[len(toks)-1]
				isTail := lastTok == "RR73" || lastTok == "RRR" || lastTok == "73" || lastTok == "CQ"
				hasGrid := !isTail && len(lastTok) >= 4 && lastTok[0] >= 'A' && lastTok[0] <= 'R' &&
					lastTok[1] >= 'A' && lastTok[1] <= 'R' &&
					lastTok[2] >= '0' && lastTok[2] <= '9' && lastTok[3] >= '0' && lastTok[3] <= '9'
				senderGrid := ""
				if hasGrid {
					senderGrid = lastTok
				} else if g, ok := gridCache[toks[1]]; ok {
					senderGrid = g
				}
				// Station bookkeeping for the selector: ">" rows are
				// messages this station SENT, "<" ones addressed TO it.
				txt := e.Text
				if len(txt) > 36 {
					txt = txt[:36]
				}
				s := station(toks[1])
				if hasGrid {
					s.grid = lastTok
				}
				s.msgs = append(s.msgs, e.Time+" > "+txt)
				if !isCQ {
					rcp := station(toks[0])
					if rcp.grid == "" {
						if g, ok := gridCache[toks[0]]; ok {
							rcp.grid = g
						}
					}
					rcp.msgs = append(rcp.msgs, e.Time+" < "+txt)
				}
				slat, slon, sApprox, sok := stationPos(toks[1], senderGrid)
				if !sok {
					continue
				}
				ent := ui.MapEntry{Lat: slat, Lon: slon, Role: ui.RoleSender, IsCQ: isCQ, Approx: sApprox, Age: age}
				// QSO arc: SENDER -> RECIPIENT. The recipient (toks[0])
				// never carries a grid inside QSO texts — exact cached
				// grid when heard before, else their country centroid.
				if !isCQ {
					if rlat, rlon, rApprox, rok := stationPos(toks[0], gridCache[toks[0]]); rok &&
						(rlat != slat || rlon != slon) {
						ent.Lat, ent.Lon, ent.Approx = rlat, rlon, rApprox
						ent.Arc = true
						ent.Role = ui.RoleReceiver
						ent.FromLat, ent.FromLon = slat, slon
					}
				}
				mapEntries = append(mapEntries, ent)
			}
			// The sorted station list drives Left/Right selection.
			mapStationNames = mapStationNames[:0]
			for call := range stations {
				mapStationNames = append(mapStationNames, call)
			}
			sort.Strings(mapStationNames)
			if mapSel >= len(mapStationNames) {
				mapSel = len(mapStationNames) - 1 // stations age out of the window
			}
			if mapSel < 0 {
				mapDetail = false
			}
			var mapSelUI *ui.MapSelection
			if mapSel >= 0 && mapSel < len(mapStationNames) {
				call := mapStationNames[mapSel]
				st := stations[call]
				if lat, lon, approx, ok := stationPos(call, st.grid); ok {
					mapSelUI = &ui.MapSelection{
						Call: call, Lat: lat, Lon: lon, Approx: approx,
						Grid: st.grid, Country: geo.Country(call),
						Index: mapSel + 1, Total: len(mapStationNames),
					}
					if mapDetail {
						mapSelUI.Detail = st.msgs
					}
				}
			}
			if os.Getenv("SDR_MAP_DEMO") != "" {
				// Dev aid: fixed entries + an open detail panel so the
				// overlay can be eyeballed from a rendered PNG.
				// SDR_MAP_STYLE / SDR_MAP_PAL pick the basemap and
				// colour set (int indexes).
				if v, err := strconv.Atoi(os.Getenv("SDR_MAP_STYLE")); err == nil {
					for i := 0; i < v; i++ {
						u.CycleMap(1)
					}
				}
				if v, err := strconv.Atoi(os.Getenv("SDR_MAP_PAL")); err == nil {
					u.SetMapPalette(v)
				}
				latT, lonT, _ := geo.GridToLatLon("OK04")
				latB, lonB, _ := geo.GridToLatLon("JO65")
				u.DrawWorldMap([]ui.MapEntry{
					{Lat: latT, Lon: lonT, IsCQ: true, Age: 2 * time.Second},
					{Lat: 50.8, Lon: 4.4, IsCQ: true, Approx: true, Age: 30 * time.Second},
					{Lat: latB, Lon: lonB, Arc: true, FromLat: latT, FromLon: lonT, Role: ui.RoleReceiver, Age: time.Minute},
				}, &ui.MapSelection{
					Call: "HS0ZKO", Lat: latT, Lon: lonT, Grid: "OK04", Country: "Thailand",
					Index: 3, Total: 7,
					Detail: []string{
						"12:00:15 > CQ HS0ZKO OK04",
						"12:00:30 < ON4ABC HS0ZKO R-07",
						"12:00:45 > ON4ABC HS0ZKO RR73",
						"12:00:50 < ON4ABC HS0ZKO JO65",
					},
				})
			} else {
				u.DrawWorldMap(mapEntries, mapSelUI)
			}
		} else if uiMode == uiFT8Bands {
			// FT8 band picker; the green row is the band currently
			// tuned (within ±2 kHz of the dial frequency).
			labels := make([]string, len(ft8Bands))
			active := -1
			for i, band := range ft8Bands {
				labels[i] = band.label
				d := r.Freq() - band.hz
				if d < 0 {
					d = -d
				}
				if d <= 2000 {
					active = i
				}
			}
			u.DrawBandList(labels, ft8BandSel, active)
		} else if uiMode == uiRTTY {
			for _, ln := range r.RTTYTakeLines() {
				rttyLines = append(rttyLines, ln)
				if len(rttyLines) > 60 {
					rttyLines = rttyLines[len(rttyLines)-60:]
				}
			}
			m, sp := r.RTTYLevels()
			u.DrawRTTY(rttyLines, r.RTTYCurrent(), m, sp, r.RTTYReversed(), r.RTTYEnabled())
		} else if uiMode == uiADSB {
			// Mercator projection view for map layers: world pixels of the
			// receiver at zoom 12 and the ground scale there. project()
			// normalises through MPerPx, so a single fixed zoom serves every
			// layer zoom — blips always land exactly on the tiles.
			zCur := adsbZooms[adsbRangeIdx]
			mrx, mry := osm.MercatorPx(adsbLat, adsbLon, zCur)
			merc := &ui.MercView{Zoom: zCur, Rx: mrx, Ry: mry, MPerPx: osm.MercMetresPx(adsbLat, zCur)}
			// MUST be the same zoom as the receiver reference: project()
			// subtracts world pixels, so mixed zooms push every target far
			// off-screen (all blips clamped to the same edge point).
			mercPos := func(lat, lon float64) (float64, float64) {
				return osm.MercatorPx(lat, lon, zCur)
			}
			planes := adsbStore.Planes()
			blips := make([]ui.RadarBlip, 0, len(planes))
			for _, pl := range planes {
				call := pl.Callsign
				if !aisShowName {
					call = adsbRegs.Lookup(pl.ICAO)
					if call == "" {
						call = pl.ICAO
					}
				}
				cc := geo.ICAOCountry(pl.ICAO)
				reg := adsbRegs.Lookup(pl.ICAO)
				b := ui.RadarBlip{Call: call, ICAO: pl.ICAO, Country: cc, AltFt: pl.AltFt, SpdKt: pl.SpeedKt, TrackDeg: pl.TrackDeg, VrateFpm: pl.VrateFpm, HasPos: pl.HasPos, Seen: pl.LastSeen, Lat: pl.Lat, Lon: pl.Lon, Reg: reg}
				if pl.HasPos {
					b.DistKm, b.BrngDeg = geo.DistanceBearingKm(adsbLat, adsbLon, pl.Lat, pl.Lon)
					b.MercX, b.MercY = mercPos(pl.Lat, pl.Lon)
				}
				for _, tp := range pl.Trail {
					d, br := geo.DistanceBearingKm(adsbLat, adsbLon, tp.Lat, tp.Lon)
					mx, my := mercPos(tp.Lat, tp.Lon)
					b.Trail = append(b.Trail, ui.RadarDot{DistKm: d, BrngDeg: br, AgeSec: int(time.Since(tp.At).Seconds()), AltFt: tp.AltFt, MercX: mx, MercY: my})
				}
				blips = append(blips, b)
			}
			for _, sh := range aisStore.Ships() {
				if !sh.HasPos {
					continue
				}
				d, br := geo.DistanceBearingKm(adsbLat, adsbLon, sh.Lat, sh.Lon)
				name := sh.MMSI
				if aisShowName && sh.Name != "" {
					name = sh.Name
				}
				mx, my := mercPos(sh.Lat, sh.Lon)
				cc := geo.MMSICountry(sh.MMSI)
				blips = append(blips, ui.RadarBlip{Vessel: true, AtoN: sh.AtoN, Call: name, ICAO: sh.MMSI, Country: cc, BrngDeg: br, DistKm: d, SogKt: sh.SogKt, HasPos: true, MercX: mx, MercY: my, Seen: sh.LastSeen,
					Lat: sh.Lat, Lon: sh.Lon, Callsign: sh.Callsign, Imo: sh.Imo, Dest: sh.Destination, Eta: sh.EtaText,
					NavStat: sh.NavStat, ShipType: sh.ShipType, Draught: sh.Draught,
					DimLen: sh.DimA + sh.DimB, DimWid: sh.DimC + sh.DimD, Heading: sh.Heading, CogDeg: sh.CogDeg})
				// Demo aids (the live Thai feed carries no types 6/21 right
				// now) so the rhombus rendering stays verifiable.
				if os.Getenv("SDR_ADSB_DEMO") != "" {
					demoNow := time.Now()
					for _, ad := range [][2]float64{{13.640, 100.560}, {13.560, 100.620}} {
						amx, amy := mercPos(ad[0], ad[1])
						dd, bb := geo.DistanceBearingKm(adsbLat, adsbLon, ad[0], ad[1])
						blips = append(blips, ui.RadarBlip{Vessel: true, AtoN: true, Call: "AID", ICAO: "992190761", BrngDeg: bb, DistKm: dd, HasPos: true, MercX: amx, MercY: amy, Seen: demoNow})
					}
				}
			}
			for _, st := range aprsStore.All() {
				d, br := geo.DistanceBearingKm(adsbLat, adsbLon, st.Lat, st.Lon)
				mx, my := mercPos(st.Lat, st.Lon)
				blips = append(blips, ui.RadarBlip{Aprs: true, Own: st.Own, Call: st.Call, Sym: string(st.Sym), Country: geo.CountryISO(st.Call),
					BrngDeg: br, DistKm: d,
					HasPos: true, MercX: mx, MercY: my, Seen: st.LastHeard,
					SogKt: st.SpeedKt, TrackDeg: int(st.CourseDeg), Lat: st.Lat, Lon: st.Lon})
			}
			if os.Getenv("SDR_ADSB_DEMO") != "" {
				// Dev aid: two sample APRS stations so the diamond
				// rendering is verifiable from a rendered PNG.
				for _, st := range []struct {
					call     string
					lat, lon float64
				}{{"HS0ABC-9", 13.62, 100.59}, {"E23AQ", 13.55, 100.50}} {
					d, br := geo.DistanceBearingKm(adsbLat, adsbLon, st.lat, st.lon)
					mx, my := mercPos(st.lat, st.lon)
					blips = append(blips, ui.RadarBlip{Aprs: true, Call: st.call, Sym: ">", Country: "TH", BrngDeg: br, DistKm: d,
						HasPos: true, MercX: mx, MercY: my, Seen: time.Now(), Lat: st.lat, Lon: st.lon})
				}
			}
			if os.Getenv("SDR_ADSB_DEMO") != "" {
				// Dev aid: fake traffic so the radar can be eyeballed
				// from a rendered PNG.
				for _, d := range []struct {
					call                      string
					brng, dist, alt, spd, trk int
					vr                        int
				}{
					{"THA341", 35, 32, 35000, 470, 75, 1200},
					{"AIH772", 128, 71, 27000, 440, 300, -800},
					{"TGK209", 255, 18, 8000, 250, 20, 0},
					{"WMS12", 300, 180, 41000, 490, 90, 0},
				} {
					b := ui.RadarBlip{Call: d.call, BrngDeg: float64(d.brng), DistKm: float64(d.dist), AltFt: d.alt, SpdKt: d.spd, TrackDeg: d.trk, VrateFpm: d.vr, HasPos: true}
					// Fake breadcrumbs: walk backwards along the track in
					// flat east/north offsets, then back to bearing/dist.
					en := float64(d.dist) * math.Sin(float64(d.brng)*math.Pi/180)
					nn := float64(d.dist) * math.Cos(float64(d.brng)*math.Pi/180)
					for k := 1; k <= 30; k++ {
						e := en - math.Sin(float64(d.trk)*math.Pi/180)*2.5*float64(k)
						n := nn - math.Cos(float64(d.trk)*math.Pi/180)*2.5*float64(k)
						td := math.Hypot(e, n)
						tb := math.Atan2(e, n) * 180 / math.Pi
						if tb < 0 {
							tb += 360
						}
						b.Trail = append(b.Trail, ui.RadarDot{BrngDeg: tb, DistKm: td, AgeSec: k * 10, AltFt: d.alt})
					}
					blips = append(blips, b)
				}
			}
			_ = aisConnected // TODO: status line
			z, L := adsbRangeIdx, adsbLayerIdx
			zoomCur := adsbZooms[z]
			// View centre = receiver offset by the pan (screen px at
			// this zoom are world px).
			rxW, ryW := osm.MercatorPx(adsbLat, adsbLon, zoomCur)
			vx, vy := rxW+float64(panX), ryW+float64(panY)
			ctr := adsbMosaicCtr[L][z]
			drift := adsbMosaic[L][z] == nil || math.Hypot(vx-ctr[0], vy-ctr[1]) > 120
			if !osm.Layers[L].NoFetch && drift && !adsbFetching[L][z] {
				adsbFetching[L][z] = true
				latC, lonC := osm.MercatorInv(vx, vy, zoomCur)
				layer := L
				go func() {
					// Larger than the screen so small pans stay covered
					// until the refetched mosaic lands.
					m := osmCache.Mosaic(layer, latC, lonC, zoomCur, 1024, 768)
					adsbMosaic[layer][z] = m
					adsbMosaicCtr[layer][z] = [2]float64{vx, vy}
					adsbFetching[layer][z] = false
				}()
			}
			// Offset of the screen inside the (larger) mosaic.
			mapOffX, mapOffY := int(vx-ctr[0])+192, int(vy-ctr[1])+144
			mapName, mapAttr := "", ""
			if !osm.Layers[L].NoFetch && adsbMosaic[L][z] != nil {
				mapName, mapAttr = osm.Layers[L].Name, osm.Layers[L].Attr+" · flags © country-flag-icons (MIT)"
			}
			var mercArg *ui.MercView
			if adsbMosaic[L][z] != nil {
				mercArg = merc
			}
			hostLbl := adsbHost
			if hostLbl == "" {
				hostLbl = "(" + i18n.T("off") + ")"
			}
			u.DrawRadar(blips, adsbRanges[adsbRangeIdx], hostLbl, adsbConnected, adsbLat, adsbLon, cpu, adsbMosaic[L][z], mapName, mapAttr, mercArg, flagDir, radarTargetsMask, radarLabelMode, panX, panY, mapOffX, mapOffY, sysinfo.SensorSnapshot().BattPct, strings.Contains(sysinfo.SensorSnapshot().BattStatus, "harg"), radarSel)
		}
		if r.FT8Enabled() && uiMode == uiMain {
			u.DrawFT8Grid(loHz, viewOff)
			u.DrawFT8Log(ft8Log, flagDir)
		}
		if r.WefaxEnabled() && uiMode == uiMain {
			u.DrawWefaxGuides()
		}
		if r.AISRFEnabled() && uiMode == uiMain && !r.FT8Enabled() {
			aisLogMu.Lock()
			view := make([]ui.AISEntry, len(aisLog))
			copy(view, aisLog)
			aisLogMu.Unlock()
			u.DrawAISLog(view, flagDir)
		}
		if r.WefaxEnabled() && uiMode == uiMain {
			prev, lines, st := r.Wefax().Preview(ui.WefaxPanelW, u.WefaxPreviewRows())
			u.DrawWefaxPanel(prev, lines, wefaxStateLabel(st), i18n.T("wefax_hint"))
		}
		if r.CWDecodeEnabled() && uiMode == uiMain {
			u.DrawCWLog(r.CW().Text(), r.CW().WPM())
		}
		if r.WefaxEnabled() {
			// Auto-save on APT stop — on any screen, so a chart that
			// finishes while a menu is open is never lost.
			for _, img := range r.Wefax().TakeDone() {
				saveWefaxFile(img, true)
			}
		}
		if uiMode == uiMain {
			u.DrawSysBadge(cpu, mem, sysinfo.SensorSnapshot().BattPct, strings.Contains(sysinfo.SensorSnapshot().BattStatus, "harg"))
		}
		lastFrame = frame
		if err := disp.Present(frame); err != nil {
			fmt.Fprintf(os.Stderr, "present: %v\n", err)
			return
		}
		frames++
		// Heartbeat: separates "app hung" from "rendering but invisible"
		// when reading a launch log.
		if time.Since(lastBeat) >= 10*time.Second {
			s := r.Snapshot()
			var af, astall int64
			if out != nil {
				af, astall = out.Stats()
			}
			fmt.Fprintf(os.Stderr, "alive: frames=%d connected=%v freq=%.4f MHz mode=%s bytes=%d audioFrames=%d maxStall=%dms\n",
				atomic.LoadUint64(&frames), s.Connected, float64(r.Freq())/1e6, r.Mode().Name, s.BytesRx, af, astall)
			lastBeat = time.Now()
		}
	}
}

// saveBwNow records the current mode's bandwidth into the in-memory
// config map (flushed to disk on quit).
func saveBwNow(cfg map[string]string, r *radio.Radio) {
	cfg[fmt.Sprintf("bw.%s", r.Mode().Name)] = fmt.Sprintf("%g", r.Bandwidth())
}

// kbRows mirrors ui.kbRows for the editor logic.
var kbRows = []string{
	"0123456789",
	"abcdefghijklm",
	"nopqrstuvwxyz",
	".:-_/ ",
}

func ft8Label(on bool) string {
	if on {
		return i18n.T("on")
	}
	return i18n.T("off")
}

func langLabel() string {
	if i18n.Lang() == "en" {
		return "English"
	}
	return "ไทย"
}

func agcLabel(on bool) string {
	if on {
		return i18n.T("on")
	}
	return i18n.T("off")
}

// ft8Seen is one recently decoded message, for duplicate suppression.
type ft8Seen struct {
	text string
	at   time.Time
}

// bwLabel formats a bandwidth value: kHz for >= 1 kHz, Hz below.
func bwLabel(hz float64) string {
	if hz >= 1000 {
		return fmt.Sprintf("%g kHz", hz/1000)
	}
	return fmt.Sprintf("%g Hz", hz)
}

// --- tiny config file ---------------------------------------------------

func mustExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return exe
}

// localIP returns the first non-loopback IPv4 address so the System
// menu can show the exact URL to open on a phone/laptop.
func localIP() string {
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
				if v4 := ipn.IP.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	return "0.0.0.0"
}

func configFile(name string) string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), name)
	}
	return name
}

func configPath() string       { return configFile("sdrg35xx.ini") }
func legacyConfigPath() string { return configFile("sdr35.ini") } // pre-rename app

func loadConfig() map[string]string {
	cfg := readIni(configPath())
	if _, ok := cfg["host"]; !ok {
		// A truncated ini (crash mid-write from the pre-atomic era):
		// "host" is always the first key written — missing means the
		// file is not whole. Prefer the backup generation.
		if bak := readIni(configPath() + ".bak"); len(bak) > len(cfg) {
			fmt.Fprintln(os.Stderr, "config: ini incomplete, using .bak")
			cfg = bak
		}
	}
	if len(cfg) == 0 {
		// First run after the SDR35 → SDRg35xx rename: adopt the old
		// settings so host/freq/gain survive.
		cfg = readIni(legacyConfigPath())
	}
	return cfg
}

func readIni(path string) map[string]string {
	cfg := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '='); i > 0 {
			cfg[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	return cfg
}

func saveConfig(cfg map[string]string, host string, freq int64, mode string, vol float64, gainDb float64, rate, spanKHz int, dsPref, agcPref, langPref string, stepHz int64, myCall, myGrid, myAnt, myRig string, pskOn bool, wfMin, wfMax float64) {
	// Atomic write: a crash or concurrent writer can never leave a
	// half-written ini (which silently dropped optional keys like the
	// host lists on reload). Write to .tmp, keep the previous file as
	// .bak, then rename into place.
	path := configPath()
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return
	}
	// Radio host: an empty host (radio disabled) persists as "off".
	hostOut := host
	if hostOut == "" {
		hostOut = "off"
	}
	fmt.Fprintf(f, "host=%s\nfreq=%d\nmode=%s\nvol=%.2f\ngain=%.1f\nrate=%d\nspan=%d\nds=%s\nagc=%s\nlang=%s\nstep=%d\n", hostOut, freq, mode, vol, gainDb, rate, spanKHz, dsPref, agcPref, langPref, stepHz)
	// Squelch level from the live config map.
	if v, ok := cfg["sql"]; ok {
		fmt.Fprintf(f, "sql=%s\n", v)
	}
	if v, ok := cfg["ppm"]; ok {
		fmt.Fprintf(f, "ppm=%s\n", v)
	}
	if v, ok := cfg["lmute"]; ok {
		fmt.Fprintf(f, "lmute=%s\n", v)
	}
	if v, ok := cfg["aisrf"]; ok {
		fmt.Fprintf(f, "aisrf=%s\n", v)
	}
	if v, ok := cfg["adsbrf"]; ok {
		fmt.Fprintf(f, "adsbrf=%s\n", v)
	}
	if v, ok := cfg["rtlsrv"]; ok {
		fmt.Fprintf(f, "rtlsrv=%s\n", v)
	}
	if v, ok := cfg["rtlsrvport"]; ok {
		fmt.Fprintf(f, "rtlsrvport=%s\n", v)
	}

	for _, k := range []string{"deepcw", "deepcwth", "deepcwwin", "ft8ts", "ft8tsdepth", "ft8tsth", "ft8tsband"} {
		if v, ok := cfg[k]; ok {
			fmt.Fprintf(f, "%s=%s\n", k, v)
		}
	}
	if v, ok := cfg["update"]; ok {
		fmt.Fprintf(f, "update=%s\n", v)
	}
	if v, ok := cfg["hosts"]; ok && v != "" {
		fmt.Fprintf(f, "hosts=%s\n", v)
	}
	if v, ok := cfg["beasthosts"]; ok && v != "" {
		fmt.Fprintf(f, "beasthosts=%s\n", v)
	}
	if v, ok := cfg["aishosts"]; ok && v != "" {
		fmt.Fprintf(f, "aishosts=%s\n", v)
	}
	if v, ok := cfg["web"]; ok {
		fmt.Fprintf(f, "web=%s\n", v)
	}
	if v, ok := cfg["webport"]; ok {
		fmt.Fprintf(f, "webport=%s\n", v)
	}
	fmt.Fprintf(f, "call=%s\ngrid=%s\npsk=%s\nantenna=%s\nrig=%s\nwfmin=%g\nwfmax=%g\n", myCall, myGrid, map[bool]string{true: "on", false: "off"}[pskOn], myAnt, myRig, wfMin, wfMax)
	if v, ok := cfg["bm"]; ok {
		fmt.Fprintf(f, "bm=%s\n", v)
	}
	if v, ok := cfg["nr"]; ok {
		fmt.Fprintf(f, "nr=%s\n", v)
	}
	if v, ok := cfg["hp"]; ok {
		fmt.Fprintf(f, "hp=%s\n", v)
	}
	if v, ok := cfg["lp"]; ok {
		fmt.Fprintf(f, "lp=%s\n", v)
	}
	if v, ok := cfg["rtty"]; ok {
		fmt.Fprintf(f, "rtty=%s\n", v)
	}
	if v, ok := cfg["wefax"]; ok {
		fmt.Fprintf(f, "wefax=%s\n", v)
	}
	if v, ok := cfg["wefaxauto"]; ok {
		fmt.Fprintf(f, "wefaxauto=%s\n", v)
	}
	if v, ok := cfg["cwdec"]; ok {
		fmt.Fprintf(f, "cwdec=%s\n", v)
	}
	if v, ok := cfg["gpsfollow"]; ok {
		fmt.Fprintf(f, "gpsfollow=%s\n", v)
	}
	if v, ok := cfg["aprs"]; ok {
		fmt.Fprintf(f, "aprs=%s\n", v)
	}
	if v, ok := cfg["aprsfreq"]; ok {
		fmt.Fprintf(f, "aprsfreq=%s\n", v)
	}
	if v, ok := cfg["aprscall"]; ok {
		fmt.Fprintf(f, "aprscall=%s\n", v)
	}
	if v, ok := cfg["aprsbeacon"]; ok {
		fmt.Fprintf(f, "aprsbeacon=%s\n", v)
	}
	if v, ok := cfg["aprspath"]; ok {
		fmt.Fprintf(f, "aprspath=%s\n", v)
	}
	if v, ok := cfg["aprssym"]; ok {
		fmt.Fprintf(f, "aprssym=%s\n", v)
	}
	if v, ok := cfg["aprscmt"]; ok {
		fmt.Fprintf(f, "aprscmt=%s\n", v)
	}
	if v, ok := cfg["aprslvl"]; ok {
		fmt.Fprintf(f, "aprslvl=%s\n", v)
	}
	if v, ok := cfg["aprspre"]; ok {
		fmt.Fprintf(f, "aprspre=%s\n", v)
	}
	if v, ok := cfg["nrnn"]; ok {
		fmt.Fprintf(f, "nrnn=%s\n", v)
	}

	if v, ok := cfg["sstv"]; ok {
		fmt.Fprintf(f, "sstv=%s\n", v)
	}
	if v, ok := cfg["aprssrc"]; ok {
		fmt.Fprintf(f, "aprssrc=%s\n", v)
	}
	if v, ok := cfg["aprsfixlat"]; ok {
		fmt.Fprintf(f, "aprsfixlat=%s\n", v)
	}
	if v, ok := cfg["aprsfixlon"]; ok {
		fmt.Fprintf(f, "aprsfixlon=%s\n", v)
	}
	if v, ok := cfg["aprsigate"]; ok {
		fmt.Fprintf(f, "aprsigate=%s\n", v)
	}
	if v, ok := cfg["aprsigatelimit"]; ok {
		fmt.Fprintf(f, "aprsigatelimit=%s\n", v)
	}

	if v, ok := cfg["aprsis"]; ok {
		fmt.Fprintf(f, "aprsis=%s\n", v)
	}
	if v, ok := cfg["aprsiserver"]; ok {
		fmt.Fprintf(f, "aprsiserver=%s\n", v)
	}

	if v, ok := cfg["gpstime"]; ok {
		fmt.Fprintf(f, "gpstime=%s\n", v)
	}
	if v, ok := cfg["adsbhost"]; ok {
		fmt.Fprintf(f, "adsbhost=%s\n", v)
	}
	if v, ok := cfg["adsblat"]; ok {
		fmt.Fprintf(f, "adsblat=%s\n", v)
	}
	if v, ok := cfg["adsblon"]; ok {
		fmt.Fprintf(f, "adsblon=%s\n", v)
	}
	if v, ok := cfg["adsblayer"]; ok {
		fmt.Fprintf(f, "adsblayer=%s\n", v)
	}
	if v, ok := cfg["aisname"]; ok {
		fmt.Fprintf(f, "aisname=%s\n", v)
	}
	if v, ok := cfg["aishost"]; ok {
		fmt.Fprintf(f, "aishost=%s\n", v)
	}
	if v, ok := cfg["updateurl"]; ok && v != "" {
		fmt.Fprintf(f, "updateurl=%s\n", v)
	}
	// Per-mode bandwidth entries from the live config map.
	for _, m := range dsp.ModeList {
		if v, ok := cfg[fmt.Sprintf("bw.%s", m.Name)]; ok {
			fmt.Fprintf(f, "bw.%s=%s\n", m.Name, v)
		}
	}
	if err := f.Close(); err != nil {
		return
	}
	os.Rename(path, path+".bak") // previous generation, kept for recovery
	if err := os.Rename(path+".tmp", path); err != nil {
		// Rename failed (odd fs): restore the old file so settings
		// are not lost entirely.
		os.Rename(path+".bak", path)
	}
}

// isSpotCallsign filters reportable callsigns: 3-11 chars, starts with
// a letter or digit, contains at least one digit (standard-form
// amateur calls) — skips hashes "<...>", telemetry and plain words.
func isSpotCallsign(s string) bool {
	if len(s) < 3 || len(s) > 11 {
		return false
	}
	hasDigit := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return hasDigit
}

// wefaxStateLabel names the decoder phase for the panel title.
func wefaxStateLabel(st dsp.WefaxState) string {
	switch st {
	case dsp.WefaxPhasing:
		return i18n.T("wefax_phasing")
	case dsp.WefaxImage:
		return i18n.T("wefax_rx")
	}
	return i18n.T("wefax_idle")
}
