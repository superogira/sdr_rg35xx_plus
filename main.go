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
//	L1/R1 volume −/+               START/SELECT  quit
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"sdr35/internal/audio"
	"sdr35/internal/dsp"
	"sdr35/internal/i18n"
	"sdr35/internal/input"
	"sdr35/internal/radio"
	"sdr35/internal/pskreporter"
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
		// Only server-verified rates; a stale 512000 from an old build
		// falls through to the default instead of requesting a rate the
		// server cannot stream cleanly.
		if n, err := strconv.Atoi(v); err == nil && (n == 2_048_000 || n == 1_024_000) {
			rate = n
		}
	}
	if v, ok := cfg["lang"]; ok {
		i18n.SetLang(v)
	}
	myCall := strings.ToUpper(strings.TrimSpace(cfg["call"]))
	myGrid := strings.ToUpper(strings.TrimSpace(cfg["grid"]))
	pskOn := cfg["psk"] == "on"
	sqlPref := 0.0
	if v, ok := cfg["sql"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 4 && f <= 40 {
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
		if n, err := strconv.Atoi(v); err == nil && n >= 10 && n <= 2048 {
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
	if span > 0 {
		u.SetSpanKHz(span)
	}
	if ds != -1 {
		r.SetDirectSamplingMode(ds)
	}
	if sqlPref > 0 {
		r.SetSquelchDb(sqlPref)
	}
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
	boot := u.Frame(ui.FrameStats{FreqHz: *freq, Mode: dspMode.Name, StepHz: 12_500, StatusText: i18n.T("starting")})
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
		saveConfig(cfg, *host, r.Freq(), r.Mode().Name, r.Volume(), *gain, r.IQRate(), u.SpanFull/1000, dsPref, agcPref, langPref, stepHz, myCall, myGrid, pskOn)
		stop()
	}
	// Screenshot support: the last presented frame and a transient status
	// message pointing at the saved file (triggered from the menu).
	ft8Log := make([]ui.FT8Entry, 0, 100)
	// recentFT8 drives the 6 s duplicate window for decoded messages.
	recentFT8 := make([]ft8Seen, 0, 40)
	// PSK Reporter: spots are buffered as they decode and flushed over
	// UDP every 5 minutes (the service asks for at most that rate).
	psk := pskreporter.New(myCall, myGrid, pskOn)
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			psk.Flush()
		}
	}()

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
	)
	uiMode := uiMain
	// Dev aid for PNG screenshot testing of the overlays.
	switch os.Getenv("SDR_UI") {
	case "menu":
		uiMode = uiMenu
	case "freq":
		uiMode = uiFreqEdit
	}
	menuSel := 0
	// Row indexes MUST match the items slice built for DrawMenu below.
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
	)
	// The flat 16-row menu outgrew the screen, so it is now three
	// subpages reached from a 3-row root. pageItems maps (page → row)
	// to the item ids that adjustItem/activateItem already dispatch on.
	const (
		pageRoot = iota
		pageRx
		pageFT8
		pageSys
	)
	menuPage := pageRoot
	pageItems := [][]int{
		{0, 0, 0}, // root rows open subpages (dispatched by row index)
		{menuFreq, menuMode, menuGain, menuSQL, menuSample, menuBW, menuDS, menuAGC, menuSpan, menuStep},
		{menuFT8, menuCall, menuGrid, menuPSK},
		{menuHost, menuLang, menuSysMon, menuVolume, menuShot, menuUpdate},
	}
	spanSteps := []int{1000, 750, 500, 250, 125, 100, 50, 25, 12, 10, 5, 3}
	spanIdx := func() int {
		want := u.SpanFull / 1000
		for i, v := range spanSteps {
			if v == want {
				return i
			}
		}
		return 2 // default 500k if unset
	}()

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
	kbTitle := func() string {
		switch kbTarget {
		case "call":
			return i18n.T("m_call")
		case "grid":
			return i18n.T("m_grid")
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

	adjustItem := func(idx, dir int) {
		switch idx {
		case menuMode:
			saveBwNow(cfg, r)
			m := dsp.NextMode(r.Mode())
			if dir < 0 {
				// one back in a 5-cycle == four forward
				m = dsp.NextMode(dsp.NextMode(dsp.NextMode(dsp.NextMode(r.Mode()))))
			}
			r.SetMode(m)
		case menuGain:
			r.SetGainDb(radio.GainStepDb(r.GainDb(), dir))
		case menuSQL:
			db := r.SquelchDb() + float64(dir)*4
			if db < 4 {
				db = 4
			}
			if db > 40 {
				db = 40
			}
			r.SetSquelchDb(db)
			cfg["sql"] = fmt.Sprintf("%g", r.SquelchDb())
		case menuSample:
			// Verified rates only (512 kHz removed — the server cannot
			// stream it cleanly, audio ran time-stretched); the change
			// reconnects with the new rate as the connection's first
			// command.
			var next int
			if r.IQRate() == 2_048_000 {
				next = 1_024_000
			} else {
				next = 2_048_000
			}
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
			spanIdx = (spanIdx + len(spanSteps) + dir) % len(spanSteps)
			u.SetSpanKHz(spanSteps[spanIdx])
		case menuStep:
			for i, s := range stepSteps {
				if s == stepHz {
					stepHz = stepSteps[(i+len(stepSteps)+dir)%len(stepSteps)]
					break
				}
			}
			cfg["step"] = strconv.FormatInt(stepHz, 10)
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
		case menuPSK:
			pskOn = !pskOn
			cfg["psk"] = map[bool]string{true: "on", false: "off"}[pskOn]
			psk.SetEnabled(pskOn)
		case menuSysMon:
			uiMode = uiSysMon
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
			// Save current mode's bandwidth before switching.
			saveBwNow(cfg, r)
			m := dsp.NextMode(r.Mode())
			r.SetMode(m)
			if bw, ok := cfg[fmt.Sprintf("bw.%s", m.Name)]; ok {
				if v, err := strconv.ParseFloat(bw, 64); err == nil {
					r.SetBandwidth(v)
				}
			}
			// Broadcast FM channels are 100 kHz apart — fine steps are
			// noise there; other modes want the usual 12.5 kHz.
			if m == dsp.ModeWFM && stepHz < 25_000 {
				stepHz = 100_000
			} else if m != dsp.ModeWFM && stepHz > 25_000 {
				stepHz = 12_500
			}
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
		case input.L1:
			v := math.Round((r.Volume()-0.01)*100) / 100
			if v < 0 {
				v = 0
			}
			r.SetVolume(v)
		case input.R1:
			v := math.Round((r.Volume()+0.01)*100) / 100
			if v > 1.5 {
				v = 1.5
			}
			r.SetVolume(v)
		case input.VolDown:
			// Side volume wheel: 1% steps for precise levels; held
			// buttons auto-repeat (see the key-repeat block below).
			v := math.Round((r.Volume()-0.01)*100) / 100
			if v < 0 {
				v = 0
			}
			r.SetVolume(v)
		case input.VolUp:
			v := math.Round((r.Volume()+0.01)*100) / 100
			if v > 1.5 {
				v = 1.5
			}
			r.SetVolume(v)
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
		case uiSysMon:
			// Any of the usual close keys backs out of the monitor.
			switch b {
			case input.B, input.Start, input.Select, input.A:
				uiMode, menuPage, menuSel = uiMenu, pageSys, 2
			}
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
					hostText += string(kbRows[hostKbR][hostKbC])
				}
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
						psk.SetStation(myCall, myGrid)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageFT8, 1
				case "grid":
					if hostText != "" {
						myGrid = strings.ToUpper(hostText)
						cfg["grid"] = myGrid
						psk.SetStation(myCall, myGrid)
					}
					hostText = ""
					uiMode, menuPage, menuSel = uiMenu, pageFT8, 2
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
					uiMode, menuPage, menuSel = uiMenu, pageFT8, 0
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
				case input.Menu:
					if ev.Down {
						menuDownAt = time.Now()
					} else {
						if time.Since(menuDownAt) < 3*time.Second {
							// Short tap: toggle the settings menu
							// (or back out of the freq editor).
							switch uiMode {
							case uiFreqEdit, uiMenu, uiFT8Log, uiHostEdit, uiHostList, uiSysMon:
								uiMode = uiMain
							default:
								uiMode = uiMenu
							}
						}
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
					handlePress(ev.Button)
					lastRepeat[ev.Button] = time.Now()
				}
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
			// Key repeat for held tuning/volume buttons (main screen
			// only): 450 ms delay, then every 150 ms.
			if uiMode == uiMain {
				for _, b := range []input.Button{input.Left, input.Right, input.Up, input.Down, input.L1, input.R1, input.VolDown, input.VolUp} {
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
			ft8Log = append(ft8Log, ui.FT8Entry{Time: now.Format("15:04:05"), SNRDb: m.SNRDb, Text: m.Text})
			if len(ft8Log) > 100 {
				ft8Log = ft8Log[len(ft8Log)-100:]
			}
			// Report to PSK Reporter: the sender is the first token of
			// the message text ("CALL CALL2 …" — for CQ messages the
			// caller follows the CQ token). Hash/telemetry texts and
			// CQ itself are skipped.
			if toks := strings.Fields(m.Text); len(toks) >= 2 {
				sender := toks[0]
				if sender == "CQ" || strings.HasPrefix(sender, "CQ_") {
					sender = toks[1]
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
		cpu, mem, swp := sysinfo.Snapshot()
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
			GainText:    r.GainText() + " · " + r.SquelchLabel(),
			Host:        r.Hostname(),
			CpuPct:      cpu,
			MemPct:      mem,
			SwpPct:      swp,
		})
		// Frequency tick ruler on the waterfall (main screen only —
		// the menu/FT8 windows cover it anyway).
		if uiMode == uiMain {
			u.DrawFreqScale(r.Freq())
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
				ui.MenuItem{Label: i18n.T("m_rxpage"), Value: "▸"},
				ui.MenuItem{Label: i18n.T("m_ft8page"), Value: "▸"},
				ui.MenuItem{Label: i18n.T("m_syspage"), Value: "▸"})
		case pageRx:
			items = append(items,
				ui.MenuItem{Label: i18n.T("m_freq"), Value: fmt.Sprintf("%.5f MHz ▸", float64(r.Freq())/1e6)},
				ui.MenuItem{Label: i18n.T("m_mode"), Value: r.Mode().Name},
				ui.MenuItem{Label: i18n.T("m_gain"), Value: fmt.Sprintf("%.1f dB", r.GainDb())},
				ui.MenuItem{Label: i18n.T("m_sql"), Value: sq},
				ui.MenuItem{Label: i18n.T("m_rate"), Value: fmt.Sprintf("%.3fM", float64(r.IQRate())/1e6)},
				ui.MenuItem{Label: i18n.T("m_bw"), Value: bwLabel(r.Bandwidth())},
				ui.MenuItem{Label: i18n.T("m_ds"), Value: r.DirectSamplingLabel()},
				ui.MenuItem{Label: i18n.T("m_agc"), Value: agcLabel(r.AGCEnabled())},
				ui.MenuItem{Label: i18n.T("m_span"), Value: fmt.Sprintf("%d kHz", u.SpanFull/1000)},
				ui.MenuItem{Label: i18n.T("m_step"), Value: stepLabel(stepHz)})
		case pageFT8:
			pskVal := i18n.T("off")
			if pskOn {
				pskVal = i18n.T("on")
			}
			items = append(items,
				ui.MenuItem{Label: i18n.T("m_ft8"), Value: ft8Label(r.FT8Enabled())},
				ui.MenuItem{Label: i18n.T("m_call"), Value: myCall},
				ui.MenuItem{Label: i18n.T("m_grid"), Value: myGrid},
				ui.MenuItem{Label: i18n.T("m_psk"), Value: pskVal})
		case pageSys:
			items = append(items,
				ui.MenuItem{Label: i18n.T("m_host"), Value: r.Hostname()},
				ui.MenuItem{Label: i18n.T("m_lang"), Value: langLabel()},
				ui.MenuItem{Label: i18n.T("m_sysmon"), Value: i18n.T("press_a")},
				ui.MenuItem{Label: i18n.T("m_vol"), Value: fmt.Sprintf("%.1f%%", r.Volume()*100)},
				ui.MenuItem{Label: i18n.T("m_shot"), Value: i18n.T("press_a")},
				ui.MenuItem{Label: i18n.T("m_update"), Value: i18n.T("press_a")})
		}
		u.DrawMenu(items, menuSel, fmt.Sprintf(i18n.T("menu_ver"), buildStamp, strings.ReplaceAll(buildTime, "_", " ")))
		} else if uiMode == uiFreqEdit {
			u.DrawFreqEditor(editDigits, editCursor)
		} else if uiMode == uiHostEdit {
			u.DrawKeyboard(kbTitle(), hostText, len(hostText), hostKbR, hostKbC)
		} else if uiMode == uiHostList {
			active := 0
			for i, h := range hostList {
				if h == *host {
					active = i
				}
			}
			u.DrawHostList(hostList, hostSel, active)
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
		}
		if r.FT8Enabled() && uiMode == uiMain {
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
	"abcdefghijklmnopqrstuvwxyz",
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

func saveConfig(cfg map[string]string, host string, freq int64, mode string, vol float64, gainDb float64, rate, spanKHz int, dsPref, agcPref, langPref string, stepHz int64, myCall, myGrid string, pskOn bool) {
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
	fmt.Fprintf(f, "call=%s\ngrid=%s\npsk=%s\n", myCall, myGrid, map[bool]string{true: "on", false: "off"}[pskOn])
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
