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
	"image"
	"math"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"sdr35/internal/audio"
	"sdr35/internal/dsp"
	"sdr35/internal/input"
	"sdr35/internal/radio"
	"sdr35/internal/ui"
)

const defaultHost = "e25wop.thddns.net:2255"

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
		if strings.EqualFold(v, "wfm") {
			dspMode = dsp.ModeWFM
		}
	}
	if v, ok := cfg["gain"]; ok && *gain == 40.0 {
		if g, err := strconv.ParseFloat(v, 64); err == nil {
			*gain = g
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
	r.SetVolume(*vol)
	go r.Run(ctx)
	defer func() {
		if out != nil {
			out.Close()
		}
	}()

	disp, err := ui.OpenDisplay(*display)
	if err != nil {
		fmt.Fprintf(os.Stderr, "display: %v\n", err)
		return
	}
	defer disp.Close()
	dw, dh := disp.Size()
	fmt.Fprintf(os.Stderr, "step: display ok %dx%d\n", dw, dh)
	u := ui.New(dw, dh)
	fmt.Fprintf(os.Stderr, "step: ui created\n")

	// Boot frame right away: a solid color on screen proves the whole
	// display path before anything else can hang, and exercises the first
	// Present (which also runs the pan + mirror logic) immediately.
	boot := u.Frame(ui.FrameStats{FreqHz: *freq, Mode: dspMode.Name, StepHz: 12_500, StatusText: "กำลังเริ่มระบบ…"})
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
		saveConfig(*host, r.Freq(), r.Mode().Name, r.Volume(), *gain)
		stop()
	}
	// Screenshot support: the last presented frame and a transient status
	// message pointing at the saved file (triggered from the menu).
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
			capturedMsg = "บันทึกภาพไม่สำเร็จ: " + err.Error()
		} else {
			capturedMsg = "บันทึกภาพแล้ว: " + path
		}
		capturedAt = time.Now()
		fmt.Fprintln(os.Stderr, capturedMsg)
	}

	// --- UI state machine: main screen / settings menu / freq editor ---
	const (
		uiMain = iota
		uiMenu
		uiFreqEdit
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
	const menuFreq = 0
	const (
		menuMode = iota + 1
		menuGain
		menuSQL
		menuSample
		menuVolume
		menuShot
	)
	menuCount := menuShot + 1

	freqDigits := func() string {
		hz := r.Freq()
		mhz := hz / 1_000_000
		frac := (hz % 1_000_000) / 10 // 5 digits of 10 Hz
		return fmt.Sprintf("%04d%05d", mhz, frac)
	}
	editDigits := freqDigits()
	editCursor := 6 // default to the 10 kHz digit (index into 9 digits)

	// Long-press exit: MENU or START held for 3s quits; a short MENU tap
	// opens/closes the settings menu.
	var menuDownAt, startDownAt time.Time
	exitHint := ""

	adjustItem := func(idx, dir int) {
		switch idx {
		case menuMode:
			if dir > 0 || r.Mode() == dsp.ModeWFM {
				if r.Mode() == dsp.ModeWFM {
					r.SetMode(dsp.ModeNFM)
				} else {
					r.SetMode(dsp.ModeWFM)
				}
			}
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
		case menuVolume:
			v := math.Round((r.Volume()+float64(dir)*0.025)*40) / 40
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
		case menuFreq:
			editDigits = freqDigits()
			uiMode = uiFreqEdit
		case menuShot:
			capture()
		default:
			adjustItem(idx, +1)
		}
	}
	commitFreq := func(digits string) {
		var mhz, frac int
		fmt.Sscanf(digits[:4], "%d", &mhz)
		fmt.Sscanf(digits[4:], "%d", &frac)
		hz := int64(mhz)*1_000_000 + int64(frac)*10
		if hz < 24_000_000 {
			hz = 24_000_000
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
			if r.Mode() == dsp.ModeWFM {
				r.SetMode(dsp.ModeNFM)
			} else {
				r.SetMode(dsp.ModeWFM)
			}
		case input.X:
			r.CycleSquelch()
		case input.L1:
			v := r.Volume() - 0.1
			if v < 0 {
				v = 0
			}
			r.SetVolume(v)
		case input.R1:
			v := r.Volume() + 0.1
			if v > 1.5 {
				v = 1.5
			}
			r.SetVolume(v)
		case input.VolDown:
			// The side volume wheel: fine steps (2.5%) for precise
			// listening levels; L1/R1 stay coarse. Rounded to the
			// 2.5% grid so repeated taps land on exact values.
			v := math.Round((r.Volume()-0.025)*40) / 40
			if v < 0 {
				v = 0
			}
			r.SetVolume(v)
		case input.VolUp:
			v := math.Round((r.Volume()+0.025)*40) / 40
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
					exitHint = fmt.Sprintf("กดค้างเพื่อออก… %.1fs", float64(3*time.Second-d)/float64(time.Second))
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

		u.NewSpectrumRow(r.Tap())
		snap := r.Snapshot()
		status := snap.StatusText
		if exitHint != "" {
			status = exitHint
		}
		if capturedMsg != "" && time.Since(capturedAt) < 3*time.Second {
			status = capturedMsg
		}
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
		})
		// Settings overlays on top of the composed frame.
		if uiMode == uiMenu {
			sq := r.SquelchLabel()
			if v := r.SquelchDb(); v >= 40 {
				sq = "ปิด (Monitor)"
			} else {
				sq = fmt.Sprintf("%.0f dB", v)
			}
			items := []ui.MenuItem{
				{Label: "ความถี่", Value: fmt.Sprintf("%.5f MHz ▸", float64(r.Freq())/1e6)},
				{Label: "โหมดรับ", Value: r.Mode().Name},
				{Label: "Gain", Value: fmt.Sprintf("%.1f dB", r.GainDb())},
				{Label: "Squelch", Value: sq},
				{Label: "Sample Rate", Value: "2.048M (server กำหนด)"},
				{Label: "วอลุ่ม", Value: fmt.Sprintf("%.1f%%", r.Volume()*100)},
				{Label: "ถ่ายภาพหน้าจอ", Value: "กด A"},
			}
			u.DrawMenu(items, menuSel)
		} else if uiMode == uiFreqEdit {
			u.DrawFreqEditor(editDigits, editCursor)
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
			fmt.Fprintf(os.Stderr, "alive: frames=%d connected=%v freq=%.4f MHz mode=%s bytes=%d\n",
				atomic.LoadUint64(&frames), s.Connected, float64(r.Freq())/1e6, r.Mode().Name, s.BytesRx)
			lastBeat = time.Now()
		}
	}
}

// --- tiny config file ---------------------------------------------------

func configFile(name string) string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), name)
	}
	return name
}

func configPath() string  { return configFile("sdrg35xx.ini") }
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

func saveConfig(host string, freq int64, mode string, vol float64, gainDb float64) {
	f, err := os.Create(configPath())
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "host=%s\nfreq=%d\nmode=%s\nvol=%.2f\ngain=%.1f\n", host, freq, mode, vol, gainDb)
}
