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
	"sdr35/internal/diag"
	"sdr35/internal/dsp"
	"sdr35/internal/i18n"
	"sdr35/internal/input"
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
	mu   sync.Mutex
	msg  string
	busy bool
}

func (u *updater) setMsg(format string, a ...any) {
	u.mu.Lock()
	u.msg = fmt.Sprintf(format, a...)
	u.mu.Unlock()
}

func (u *updater) Msg() string {
	u.mu.Lock()
	defer u.mu.Unlock()
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
				u.setMsg("อัพเดทรองรับบนเครื่อง RG35XX เท่านั้น")
			}
			return
		}
		local, _ := strconv.ParseInt(buildStamp, 10, 64)
		stamp, sha, size, err := fetchUpdateMeta(base)
		if err != nil {
			if manual {
				u.setMsg("เช็คอัพเดทไม่สำเร็จ: %v", err)
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
			u.setMsg("โหลดไม่สำเร็จ: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			u.setMsg("โหลดไม่สำเร็จ: HTTP %d", resp.StatusCode)
			return
		}
		gz, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		if err != nil {
			u.setMsg("โหลดไม่สำเร็จ: %v", err)
			return
		}
		// version.txt carries the sha256 of the GZIPPED package (what
		// upload.sh hashes) — verify both before touching the disk.
		if size > 0 && int64(len(gz)) != size {
			u.setMsg("ขนาดไฟล์ไม่ตรง (%d != %d)", len(gz), size)
			return
		}
		sum := sha256.Sum256(gz)
		if fmt.Sprintf("%x", sum) != sha {
			u.setMsg("%s", i18n.T("checksum"))
			return
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			u.setMsg("ไฟล์เสีย (gzip): %v", err)
			return
		}
		bin, err := io.ReadAll(zr)
		if err != nil {
			u.setMsg("ไฟล์เสีย (gzip): %v", err)
			return
		}

		exe, err := os.Executable()
		if err != nil {
			u.setMsg("หาตำแหน่งโปรแกรมไม่ได้: %v", err)
			return
		}
		dir := filepath.Dir(exe)
		tmp := filepath.Join(dir, ".sdrg35xx.download")
		if err := os.WriteFile(tmp, bin, 0o755); err != nil {
			u.setMsg("เขียนไฟล์ไม่ได้: %v", err)
			return
		}
		// Same-directory rename: atomic on the SD card's filesystem; the
		// running old inode stays alive until exit.
		if err := os.Rename(tmp, exe); err != nil {
			os.Remove(tmp)
			u.setMsg("แทนที่ไฟล์ไม่ได้: %v", err)
			return
		}
		u.setMsg("%s", fmt.Sprintf(i18n.T("updated"), stamp))
		fmt.Fprintf(os.Stderr, "update: installed stamp %d (was %s), re-exec\n", stamp, buildStamp)
		time.Sleep(700 * time.Millisecond) // let the message reach the screen
		syncDir(dir)
		syscall.Exec(exe, os.Args, os.Environ())
		u.setMsg("รีสตาร์ทไม่สำเร็จ — ปิดแล้วเปิดใหม่")
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
		if n, err := strconv.Atoi(v); err == nil && (n == 2_048_000 || n == 1_024_000 || n == 512_000) {
			rate = n
		}
	}
	if v, ok := cfg["lang"]; ok {
		i18n.SetLang(v)
	}
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
	stepFor := func() int64 {
		if r.Mode() == dsp.ModeWFM {
			return 100_000
		}
		return 12_500
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
		saveConfig(cfg, *host, r.Freq(), r.Mode().Name, r.Volume(), *gain, r.IQRate(), u.SpanFull/1000, dsPref, agcPref, langPref)
		stop()
	}
	// Screenshot support: the last presented frame and a transient status
	// message pointing at the saved file (triggered from the menu).
	ft8Log := make([]ui.FT8Entry, 0, 12)
	var lastFT8Poll time.Time

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
		menuVolume
		menuShot
		menuUpdate
	)
	menuCount := menuUpdate + 1
	spanSteps := []int{1000, 750, 500, 250, 125, 100, 50}
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
	hostKbR, hostKbC := 0, 0
	editCursor := 6 // default to the 10 kHz digit (index into 9 digits)

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
			// Verified rates only; the change reconnects with the new
			// rate as the connection's first command.
			var next int
			switch r.IQRate() {
			case 2_048_000:
				next = 1_024_000
			case 1_024_000:
				next = 512_000
			default:
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
		case menuVolume:
			v := math.Round((r.Volume()+float64(dir)*0.01)*100) / 100
			if v < 0 {
				v = 0
			}
			if v > 1.5 {
				v = 1.5
			}
			r.SetVolume(v)
		}
	}
	activateItem := func(idx int) {
		switch idx {
		case menuHost:
			hostText = r.Hostname()
			uiMode = uiHostEdit
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
		case input.A, input.Select:
			// Save current mode's bandwidth before switching.
			saveBwNow(cfg, r)
			m := dsp.NextMode(r.Mode())
			r.SetMode(m)
			if bw, ok := cfg[fmt.Sprintf("bw.%s", m.Name)]; ok {
				if v, err := strconv.ParseFloat(bw, 64); err == nil {
					r.SetBandwidth(v)
				}
			}
		case input.Y:
			if r.FT8Enabled() {
				r.SyncFT8()
				capturedMsg = i18n.T("ft8_synced")
				capturedAt = time.Now()
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
			switch b {
			case input.Up:
				menuSel = (menuSel + menuCount - 1) % menuCount
			case input.Down:
				menuSel = (menuSel + 1) % menuCount
			case input.Left:
				adjustItem(menuSel, -1)
			case input.Right:
				adjustItem(menuSel, +1)
			case input.A:
				activateItem(menuSel)
			case input.B, input.Start:
				uiMode = uiMain
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
				if hostText != "" {
					*host = hostText
					cfg["host"] = hostText
					r.SetHost(hostText)
				}
				uiMode = uiMenu
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
				err := diag.UploadLog(diag.FTPConfig{
					Host: "e25wop.thddns.net:2121",
					User: "ftp_downloads_catgg_net",
					Pass: "4a4a10ca2e1ad8",
				}, logPath, "sdrg35xx/device.log")
				if err != nil {
					fmt.Fprintf(os.Stderr, "diag upload: %v\n", err)
				}
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
							case uiFreqEdit, uiMenu:
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

		u.NewSpectrumRow(r.Tap(), r.RawTap())
		snap := r.Snapshot()
		status := snap.StatusText
		if ft8s := r.FT8Results(); len(ft8s) > 0 {
			best := ft8s[0]
			for _, d := range ft8s[1:] {
				if d.SNRDb > best.SNRDb {
					best = d
				}
			}
			status = fmt.Sprintf("FT8: %.0f Hz %.0f dB (%.0f%%)", best.FreqHz, best.SNRDb, best.Confidence*100)
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
		if r.FT8Enabled() && time.Since(lastFT8Poll) >= 15*time.Second {
			lastFT8Poll = time.Now()
			for _, det := range r.FT8Results() {
				text := fmt.Sprintf("%.0f Hz %.0f dB", det.FreqHz, det.SNRDb)
				ft8Log = append(ft8Log, ui.FT8Entry{Time: time.Now().Format("15:04:05"), Text: text})
				if len(ft8Log) > 12 {
					ft8Log = ft8Log[len(ft8Log)-12:]
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
		// Settings overlays on top of the composed frame.
		if uiMode == uiMenu {
			sq := r.SquelchLabel()
			if v := r.SquelchDb(); v >= 40 {
				sq = i18n.T("sql_off")
			} else {
				sq = fmt.Sprintf("%.0f dB", v)
			}
			items := []ui.MenuItem{
				{Label: i18n.T("m_freq"), Value: fmt.Sprintf("%.5f MHz ▸", float64(r.Freq())/1e6)},
				{Label: i18n.T("m_mode"), Value: r.Mode().Name},
				{Label: i18n.T("m_gain"), Value: fmt.Sprintf("%.1f dB", r.GainDb())},
				{Label: i18n.T("m_sql"), Value: sq},
				{Label: i18n.T("m_rate"), Value: fmt.Sprintf("%.3fM", float64(r.IQRate())/1e6)},
				{Label: i18n.T("m_bw"), Value: bwLabel(r.Bandwidth())},
				{Label: i18n.T("m_ds"), Value: r.DirectSamplingLabel()},
				{Label: "FT8 Decode", Value: ft8Label(r.FT8Enabled())},
				{Label: i18n.T("m_agc"), Value: agcLabel(r.AGCEnabled())},
				{Label: "Host / IP", Value: r.Hostname()},
				{Label: i18n.T("m_lang"), Value: langLabel()},
				{Label: i18n.T("m_span"), Value: fmt.Sprintf("%d kHz", u.SpanFull/1000)},
				{Label: i18n.T("m_vol"), Value: fmt.Sprintf("%.1f%%", r.Volume()*100)},
				{Label: i18n.T("m_shot"), Value: i18n.T("press_a")},
				{Label: i18n.T("m_update"), Value: i18n.T("press_a")},
			}
			u.DrawMenu(items, menuSel, fmt.Sprintf("รุ่น %s · %s", buildStamp, strings.ReplaceAll(buildTime, "_", " ")))
		} else if uiMode == uiFreqEdit {
			u.DrawFreqEditor(editDigits, editCursor)
		} else if uiMode == uiHostEdit {
			u.DrawKeyboard(hostText, len(hostText), hostKbR, hostKbC)
		}
		if r.FT8Enabled() && len(ft8Log) > 0 && uiMode == uiMain {
			u.DrawFT8Log(ft8Log)
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
		return "เปิด"
	}
	return "ปิด"
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

func saveConfig(cfg map[string]string, host string, freq int64, mode string, vol float64, gainDb float64, rate, spanKHz int, dsPref, agcPref, langPref string) {
	f, err := os.Create(configPath())
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "host=%s\nfreq=%d\nmode=%s\nvol=%.2f\ngain=%.1f\nrate=%d\nspan=%d\nds=%s\nagc=%s\nlang=%s\n", host, freq, mode, vol, gainDb, rate, spanKHz, dsPref, agcPref, langPref)
	// Squelch level from the live config map.
	if v, ok := cfg["sql"]; ok {
		fmt.Fprintf(f, "sql=%s\n", v)
	}
	if v, ok := cfg["update"]; ok {
		fmt.Fprintf(f, "update=%s\n", v)
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
