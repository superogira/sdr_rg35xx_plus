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
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"flag"
	"fmt"
	"image"
	"io"
	"math"
	"net/http"
	"os"
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

	"sdr35/internal/audio"
	"sdr35/internal/backlight"
	"sdr35/internal/dsp"
	"sdr35/internal/geo"
	"sdr35/internal/i18n"
	"sdr35/internal/input"
	"sdr35/internal/pskreporter"
	"sdr35/internal/radio"
	"sdr35/internal/sysinfo"
	"sdr35/internal/ui"
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
func runUpdate(u *updater, base string, manual bool) {
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
		u.setMsg("%s", fmt.Sprintf(i18n.T("updated"), stamp))
		fmt.Fprintf(os.Stderr, "update: installed stamp %d (was %s), re-exec\n", stamp, buildStamp)
		time.Sleep(700 * time.Millisecond) // let the message reach the screen
		syncDir(dir)
		syscall.Exec(exe, os.Args, os.Environ())
		u.setMsg("%s", i18n.T("upd_restart"))
	}()
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
)

// pageItems is package-level so a test can pin it: one row list per
// page, indexed by the page ids above. A silent edit once left the
// root page at four rows while a fifth page existed — the Audio row
// was unreachable from the d-pad.
var pageItems = [][]int{
	{0, 0, 0, 0, 0}, // root rows open subpages (dispatched by row index)
	{menuFreq, menuMode, menuGain, menuSQL, menuSample, menuBW, menuDS, menuAGC, menuSpan, menuStep, menuWFMin, menuWFMax},
	{menuFT8, menuBands, menuCall, menuGrid, menuAnt, menuRig, menuPSK, menuMap},
	{menuHost, menuLang, menuSysMon, menuLogs, menuVolume, menuShot, menuUpdate},
	{menuBM},
	{menuNR, menuHP, menuLP},
}

// The flat 16-row menu outgrew the screen, so it is now three
// subpages reached from a 3-row root. pageItems maps (page → row)
// to the item ids that adjustItem/activateItem already dispatch on.
const (
	pageRoot = iota
	pageRx
	pageFT8
	pageSys
	pageBM
	pageAudio
)

func main() {
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
		*host = v
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
	if rate != 2_048_000 {
		r.SetCaptureRate(rate)
	}
	if v, ok := cfg["vol"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1.5 {
			*vol = f
		}
	}
	r.SetVolume(*vol)
	go r.Run(ctx)
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
	if strings.EqualFold(cfg["update"], "off") {
		fmt.Fprintln(os.Stderr, "update: auto-check disabled by config")
	} else {
		runUpdate(upd, updateBase, false)
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
	// Waterfall colour range (menu-adjustable, persisted).
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
	quit := func() {
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
		stop()
	}
	// Screenshot support: the last presented frame and a transient status
	// message pointing at the saved file (triggered from the menu).
	ft8Log := make([]ui.FT8Entry, 0, 100)
	// recentFT8 drives the 6 s duplicate window for decoded messages.
	recentFT8 := make([]ft8Seen, 0, 40)
	// gridCache remembers each station's grid from their CQ/contact
	// messages, so report/RRR/73 messages (which don't carry a grid)
	// can still show the distance.
	gridCache := map[string]string{}
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
	// Log viewer state: the app's own log file, re-read every 2 s.
	logLines := []string{}
	logScroll := 0
	logReadAt := time.Time{}
	var lastFrame *image.RGBA
	capturedMsg := ""
	var capturedAt time.Time
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
			capturedMsg = i18n.T("shot_fail") + err.Error()
		} else {
			capturedMsg = i18n.T("shot_ok") + path
		}
		capturedAt = time.Now()
		fmt.Fprintln(os.Stderr, capturedMsg)
	}

	// --- UI state machine: main screen / settings menu / freq editor ---
	const (
		uiMain = iota
		uiMenu
		uiFreqEdit
		uiHostEdit
		uiHostList
		uiFT8Log
		uiSysMon
		uiLogs
		uiBmList
		uiMap
		uiFT8Bands
	)
	uiMode := uiMain
	// Map screen: selected station (index into mapStationNames, -1 =
	// none) and whether the info panel is open. mapStationNames is
	// rebuilt by the map renderer each frame and read by handlePress.
	mapSel, mapDetail := -1, false
	mapStationNames := []string{}
	// FT8 band picker: selected row (up/down walk the ft8Bands table).
	ft8BandSel := 0
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
	}
	menuSel := 0
	menuPage := pageRoot
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
		case "grid":
			return i18n.T("m_grid")
		case "ant":
			return i18n.T("m_ant")
		case "rig":
			return i18n.T("m_rig")
		case "bm":
			return i18n.T("m_bm")
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

	// Long-press exit: MENU or START held for 3s quits; a short MENU tap
	// opens/closes the settings menu.
	var menuDownAt, startDownAt time.Time
	exitHint := ""
	// MENU+START held together = screenshot (any screen). Latches once
	// per joint press; menuInCombo suppresses Menu's short-tap toggle on
	// release so the combo doesn't also open the settings menu.
	shotCombo := false
	menuInCombo := false

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
			rates := []int{256_000, 1_024_000, 1_536_000, 1_792_000, 2_048_000, 2_560_000, 2_880_000, 3_200_000}
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
		case menuLang:
			if i18n.Lang() == "th" {
				i18n.SetLang("en")
			} else {
				i18n.SetLang("th")
			}
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
			wfMin += float64(dir)
			if wfMin < 0 {
				wfMin = 0
			}
			if wfMin > 40 {
				wfMin = 40
			}
			u.SetWaterfallRange(wfMin, wfMax)
			cfg["wfmin"] = fmt.Sprintf("%g", wfMin)
		case menuWFMax:
			wfMax += float64(dir) * 2
			if wfMax < 10 {
				wfMax = 10
			}
			if wfMax > 120 {
				wfMax = 120
			}
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
		case menuPSK:
			pskOn = !pskOn
			cfg["psk"] = map[bool]string{true: "on", false: "off"}[pskOn]
			psk.SetEnabled(pskOn)
		}
	}
	activateItem := func(idx int) {
		if menuPage == pageRoot {
			// Root rows open subpages by position.
			menuPage = menuSel + 1
			menuSel = 0
			return
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
			runUpdate(upd, updateBase, true)
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
			r.SetFreq(r.Freq() + 10*stepFor())
		case input.Down:
			r.SetFreq(r.Freq() - 10*stepFor())
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
			// Big scrollable FT8 history window (mode cycling moved to
			// A alone).
			if r.FT8Enabled() {
				ft8Scroll = 0
				uiMode = uiFT8Log
			}
		case input.Y:
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
				uiMode, menuPage, menuSel = uiMenu, pageRoot, 3
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
					uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuMap)
				}
			case input.Start, input.Select:
				mapSel, mapDetail = -1, false
				uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuMap)
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
			// Rows: saved hosts + "add new" at the bottom.
			rows := len(hostList) + 1
			switch b {
			case input.Up:
				hostSel = (hostSel + rows - 1) % rows
			case input.Down:
				hostSel = (hostSel + 1) % rows
			case input.A:
				if hostSel == len(hostList) {
					hostText, hostEditIdx, kbTarget = "", -1, "host"
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				} else {
					h := hostList[hostSel]
					*host = h
					cfg["host"] = h
					r.SetHost(h)
					saveHosts()
					uiMode = uiMenu
				}
			case input.X:
				if hostSel < len(hostList) {
					hostText, hostEditIdx, kbTarget = hostList[hostSel], hostSel, "host"
					hostKbR, hostKbC = 0, 0
					uiMode = uiHostEdit
				}
			case input.Y:
				if hostSel < len(hostList) {
					hostList = append(hostList[:hostSel], hostList[hostSel+1:]...)
					if len(hostList) == 0 {
						hostList = []string{*host}
					}
					if hostSel >= len(hostList) {
						hostSel = len(hostList)
					}
					saveHosts()
				}
			case input.B, input.Start:
				uiMode = uiMenu
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
				case "call":
					if hostText != "" {
						myCall = strings.ToUpper(hostText)
						cfg["call"] = myCall
						psk.SetStation(myCall, myGrid, myAnt, myRig)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuCall)
				case "grid":
					if hostText != "" {
						myGrid = strings.ToUpper(hostText)
						cfg["grid"] = myGrid
						psk.SetStation(myCall, myGrid, myAnt, myRig)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuGrid)
				case "ant":
					myAnt = hostText
					cfg["antenna"] = myAnt
					psk.SetStation(myCall, myGrid, myAnt, myRig)
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuAnt)
				case "rig":
					myRig = hostText
					cfg["rig"] = myRig
					psk.SetStation(myCall, myGrid, myAnt, myRig)
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageFT8, menuRow(pageFT8, menuRig)
				case "bm":
					if bmSel < len(bookmarks) {
						bookmarks[bmSel].label = hostText
						saveBookmarks()
					}
					hostText = ""
					uiMode = uiBmList
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
					}
					hostEditIdx = -1
					uiMode = uiHostList
				}
			case input.Start:
				// abandon the edit
				hostText = ""
				if kbTarget == "host" {
					uiMode = uiHostList
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

	for {
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
					case input.VolDown:
						v := r.Volume() - 0.001
						if v < 0 {
							v = 0
						}
						r.SetVolume(v)
					case input.VolUp:
						v := r.Volume() + 0.001
						if v > 1.5 {
							v = 1.5
						}
						r.SetVolume(v)
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
			// Hold-to-exit: MENU or START held 3 s.
			exitHint = ""
			if !menuDownAt.IsZero() || !startDownAt.IsZero() {
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
			// — instantaneous tap = 0.1%; held = exponential ramp.
			for _, b := range []input.Button{input.VolDown, input.VolUp} {
				if held[b] && time.Since(lastRepeat[b]) > (func() time.Duration {
					if volHeldAt[b].IsZero() {
						return 450 * time.Millisecond
					}
					return 150 * time.Millisecond
				})() {
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
		if newRow := u.NewSpectrumRow(r.Tap(), r.RawTap()); newRow && ft8SlotMark {
			u.MarkFT8Slot(time.Now().Format("2006-01-02 15:04:05"))
			ft8SlotMark = false
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
			ft8Log = append(ft8Log, ui.FT8Entry{Time: now.Format("15:04:05"), SNRDb: m.SNRDb, FreqHz: m.FreqHz, Text: m.Text, Anno: anno})
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
			SSBOneSided: r.Mode().SSB && r.Mode().Name != "LSB",
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
			switch menuPage {
			case pageRoot:
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_rxpage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_ft8page"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_syspage"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_bm"), Value: ">"},
					ui.MenuItem{Label: i18n.T("m_audiopage"), Value: ">"})
			case pageRx:
				freqDec := 5
				switch r.Mode().Name {
				case "WFM", "AM":
					freqDec = 3
				case "NFM", "USB", "LSB", "CW":
					freqDec = 4
				}
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_freq"), Value: fmt.Sprintf("%.*f MHz >", freqDec, float64(r.Freq())/1e6)},
					func() ui.MenuItem {
						m := ui.MenuItem{Label: i18n.T("m_mode"), Value: r.Mode().Name}
						if r.FT8Enabled() {
							m.Value = "USB (FT8)"
						}
						return m
					}(),
					ui.MenuItem{Label: i18n.T("m_gain"), Value: fmt.Sprintf("%.1f dB", r.GainDb())},
					ui.MenuItem{Label: i18n.T("m_sql"), Value: sq},
					ui.MenuItem{Label: i18n.T("m_rate"), Value: fmt.Sprintf("%.3fM", float64(r.IQRate())/1e6)},
					ui.MenuItem{Label: i18n.T("m_bw"), Value: bwLabel(r.Bandwidth())},
					ui.MenuItem{Label: i18n.T("m_ds"), Value: r.DirectSamplingLabel()},
					ui.MenuItem{Label: i18n.T("m_agc"), Value: agcLabel(r.AGCEnabled())},
					ui.MenuItem{Label: i18n.T("m_span"), Value: fmt.Sprintf("%d kHz", u.SpanFull/1000)},
					ui.MenuItem{Label: i18n.T("m_step"), Value: stepLabel(stepHz)},
					ui.MenuItem{Label: i18n.T("m_wfmin"), Value: fmt.Sprintf("+%.0f dB", wfMin)},
					ui.MenuItem{Label: i18n.T("m_wfmax"), Value: fmt.Sprintf("%.0f dB", wfMax)})
			case pageFT8:
				pskVal := i18n.T("off")
				if pskOn {
					pskVal = i18n.T("on")
				}
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_ft8"), Value: ft8Label(r.FT8Enabled())},
					ui.MenuItem{Label: i18n.T("m_bands"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_call"), Value: myCall},
					ui.MenuItem{Label: i18n.T("m_grid"), Value: myGrid},
					ui.MenuItem{Label: i18n.T("m_ant"), Value: myAnt},
					ui.MenuItem{Label: i18n.T("m_rig"), Value: myRig},
					ui.MenuItem{Label: i18n.T("m_psk"), Value: pskVal},
					ui.MenuItem{Label: i18n.T("m_map"), Value: i18n.T("press_a")})
			case pageSys:
				items = append(items,
					ui.MenuItem{Label: i18n.T("m_host"), Value: r.Hostname()},
					ui.MenuItem{Label: i18n.T("m_lang"), Value: langLabel()},
					ui.MenuItem{Label: i18n.T("m_sysmon"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_logs"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_vol"), Value: fmt.Sprintf("%.1f%%", r.Volume()*100)},
					ui.MenuItem{Label: i18n.T("m_shot"), Value: i18n.T("press_a")},
					ui.MenuItem{Label: i18n.T("m_update"), Value: i18n.T("press_a")})
			}
			u.DrawMenu(items, menuSel, fmt.Sprintf(i18n.T("menu_ver"), buildStamp, strings.ReplaceAll(buildTime, "_", " ")))
		} else if uiMode == uiFreqEdit {
			u.DrawFreqEditor(editDigits, editCursor)
		} else if uiMode == uiHostEdit {
			u.DrawKeyboard(kbTitle(), hostText, len(hostText), hostKbR, hostKbC, kbShifted)
		} else if uiMode == uiHostList {
			active := 0
			for i, h := range hostList {
				if h == *host {
					active = i
				}
			}
			u.DrawHostList(hostList, hostSel, active)
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
			u.DrawFT8LogFull(ft8Log, ft8Scroll)
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
				i18n.T("sm_batt_lvl") + "\t" + fmt.Sprintf("%d %%", sn.BattPct),
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
		}
		if r.FT8Enabled() && uiMode == uiMain {
			u.DrawFT8Grid(loHz, viewOff)
			u.DrawFT8Log(ft8Log)
		}
		if uiMode == uiMain {
			u.DrawSysBadge(cpu, mem, sysinfo.SensorSnapshot().BattPct)
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
	f, err := os.Create(configPath())
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "host=%s\nfreq=%d\nmode=%s\nvol=%.2f\ngain=%.1f\nrate=%d\nspan=%d\nds=%s\nagc=%s\nlang=%s\nstep=%d\n", host, freq, mode, vol, gainDb, rate, spanKHz, dsPref, agcPref, langPref, stepHz)
	// Squelch level from the live config map.
	if v, ok := cfg["sql"]; ok {
		fmt.Fprintf(f, "sql=%s\n", v)
	}
	if v, ok := cfg["update"]; ok {
		fmt.Fprintf(f, "update=%s\n", v)
	}
	if v, ok := cfg["hosts"]; ok && v != "" {
		fmt.Fprintf(f, "hosts=%s\n", v)
	}
	fmt.Fprintf(f, "call=%s\ngrid=%s\npsk=%s\nantenna=%s\nrig=%s\nwfmin=%g\nwfmax=%g\n", myCall, myGrid, map[bool]string{true: "on", false: "off"}[pskOn], myAnt, myRig, wfMin, wfMax)
	if v, ok := cfg["bm"]; ok {
		fmt.Fprintf(f, "bm=%s\n", v)
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
