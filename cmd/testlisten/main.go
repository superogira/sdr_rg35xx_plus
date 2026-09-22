// testlisten — offline verification of the sdr35 receive chain against a
// real rtl_tcp server (or a saved raw-IQ file). It scans the broadcast band
// for strong stations, captures WFM/NFM audio through the same dsp.Chain
// the device app uses, writes a WAV, and prints enough measurements to tell
// "real demodulated FM" (19 kHz stereo pilot, sane RMS) from "pipeline that
// just moves bytes".
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"time"

	"sdr35/internal/audio"
	"sdr35/internal/dsp"
	"sdr35/internal/rtltcp"
)

func main() {
	host := flag.String("host", "e25wop.thddns.net:2255", "rtl_tcp server")
	freq := flag.Int64("freq", 0, "capture frequency Hz (0 = auto: scan and pick the strongest)")
	mode := flag.String("mode", "wfm", "wfm | nfm")
	seconds := flag.Float64("seconds", 10, "capture duration")
	scan := flag.Bool("scan", false, "only scan the broadcast band and exit")
	ratecheck := flag.Bool("ratecheck", false, "measure the actual stream rate and exit")
	probe := flag.Bool("probe", false, "one-shot level/contrast probe on a fresh connection")
	lo := flag.Int64("lo", 87_500_000, "scan lower edge Hz")
	hi := flag.Int64("hi", 108_000_000, "scan upper edge Hz")
	step := flag.Int64("step", 400_000, "scan step Hz")
	gain := flag.Int("gain", -1, "manual tuner gain index (-1 = AGC)")
	rate := flag.Int("rate", 2_048_000, "capture rate in Hz (applied as first command)")
	out := flag.String("out", "capture.wav", "output WAV file")
	iqFile := flag.String("save-iq", "", "also save the raw IQ stream to this file")
	flag.Parse()

	dspMode := dsp.ModeWFM
	if *mode == "nfm" {
		dspMode = dsp.ModeNFM
	}

	fmt.Printf("connecting %s …\n", *host)
	start := time.Now()
	c, err := rtltcp.Dial(*host, 8*time.Second)
	if err != nil {
		fmt.Println("CONNECT FAILED:", err)
		os.Exit(1)
	}
	defer c.Close()
	fmt.Printf("connected in %v — magic=%s tunerType=%d gainCount=%d\n",
		time.Since(start).Round(time.Millisecond), c.Info.Magic, c.Info.TunerType, c.Info.GainCount)

	if *ratecheck {
		rateCheck(c, *gain)
		return
	}

	if *probe {
		// Fresh-connection one-shot: configure once, then measure — the
		// reliable ordering for this server build.
		if *freq == 0 {
			*freq = 145_100_000
		}
		_ = c.SetFrequency(uint32(*freq))
		if *gain < 0 {
			_ = c.SetTunerAGC(true)
		} else {
			_ = c.SetTunerAGC(false)
			_ = c.SetGainByIndex(*gain)
		}
		drain(c, 400*time.Millisecond)
		buf := make([]byte, 8192*2*8)
		c.ReadIQFull(buf)
		c.ReadIQFull(buf)
		var p float64
		var hist [256]int
		for j := 0; j+1 < len(buf); j += 2 {
			iv := (float64(buf[j]) - 127.5) / 127.5
			qv := (float64(buf[j+1]) - 127.5) / 127.5
			p += iv*iv + qv*qv
			hist[buf[j]]++
			hist[buf[j+1]]++
		}
		n := float64(len(buf))
		fmt.Printf("level %.1f dBFS  rail=%.1f%%  range=[%d..%d]  gain flag=%d\n",
			10*math.Log10(p/n+1e-12), 100*float64(hist[0]+hist[255])/n,
			firstNonZero(hist[:]), lastNonZero(hist[:]), *gain)

		// Spectral contrast over the same buffer.
		const nfft = 4096
		re := make([]float64, nfft)
		im := make([]float64, nfft)
		off := len(buf) - nfft*2
		for i := 0; i < nfft; i++ {
			re[i] = (float64(buf[off+2*i]) - 127.5) / 127.5
			im[i] = (float64(buf[off+2*i+1]) - 127.5) / 127.5
		}
		dsp.HannWindow(re, im)
		dsp.FFT(re, im)
		var mags []float64
		for i := 0; i < nfft/2; i++ {
			mags = append(mags, 20*math.Log10(math.Hypot(re[(i+nfft/2)%nfft], im[(i+nfft/2)%nfft])+1e-12))
		}
		sorted := append([]float64(nil), mags...)
		sort.Float64s(sorted)
		fmt.Printf("contrast peak8/median = %.1f dB\n", sorted[len(sorted)-8]-sorted[len(sorted)/2])
		return
	}

	if err := c.SetSampleRate(uint32(dsp.IQRate)); err != nil {
		fmt.Println("set rate failed:", err)
		os.Exit(1)
	}
	if *rate != 2_048_000 {
		dsp.SetIQRate(*rate)
		if err := c.SetSampleRate(uint32(*rate)); err != nil {
			fmt.Println("set rate failed:", err)
			os.Exit(1)
		}
	}
	if err := c.SetTunerAGC(*gain < 0); err != nil {
		fmt.Println("set agc failed:", err)
	}
	if *gain >= 0 {
		if err := c.SetGainByIndex(*gain); err != nil {
			fmt.Println("set gain failed:", err)
		}
	}
	_ = c.SetRTLAGC(true)

	target := *freq
	if *scan || target == 0 {
		stations := scanBand(c, *lo, *hi, *step)
		if *scan {
			return
		}
		if len(stations) == 0 {
			fmt.Println("no stations found above noise; capturing at default freq")
			target = 100_500_000
		} else {
			target = stations[0].freq
			fmt.Printf("strongest: %.1f MHz (%.1f dBFS)\n", float64(target)/1e6, stations[0].power)
		}
	}

	if dspMode == dsp.ModeNFM && *freq == 0 {
		// NFM auto: nothing sensible to scan generically; use target as-is.
	}

	fmt.Printf("capturing %.1f MHz for %.1fs (%s) …\n", float64(target)/1e6, *seconds, dspMode.Name)
	if err := c.SetFrequency(uint32(target)); err != nil {
		fmt.Println("set freq failed:", err)
		os.Exit(1)
	}

	// Discard ~300 ms of settling, then capture.
	drain(c, 300*time.Millisecond)

	chain := dsp.NewChain(dspMode, nil, nil)
	var audioOut []float32
	var raw []byte
	buf := make([]byte, 128*1024)
	deadline := time.Now().Add(time.Duration(*seconds * float64(time.Second)))
	var got int
	for time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(6 * time.Second))
		n, err := c.ReadIQ(buf)
		if n > 0 {
			got += n
			chain.Process(buf[:n], &audioOut)
			if *iqFile != "" {
				raw = append(raw, buf[:n]...)
			}
		}
		if err != nil {
			fmt.Println("stream error:", err)
			break
		}
	}
	dur := float64(got/2) / float64(dsp.IQRate)
	fmt.Printf("received %.1f MB = %.2fs of IQ (%.0f%% of wall time)\n",
		float64(got)/1e6, dur, 100*dur / *seconds)

	if *iqFile != "" {
		if err := os.WriteFile(*iqFile, raw, 0644); err != nil {
			fmt.Println("save iq:", err)
		} else {
			fmt.Println("raw IQ saved:", *iqFile)
			ifSpectrum(raw)
		}
	}

	if len(audioOut) == 0 {
		fmt.Println("NO AUDIO PRODUCED")
		os.Exit(1)
	}
	analyzeAudio(audioOut, dspMode)
	fmt.Printf("squelch end state: open=%v power=%.1f dBFS floor≈%.1f dBFS\n",
		chain.SquelchOpen(), chain.PowerDb(), chain.PowerDb()-8)

	f, err := os.Create(*out)
	if err != nil {
		fmt.Println("create wav:", err)
		os.Exit(1)
	}
	if err := audio.WriteWAVMono(f, dsp.AudioRate, audioOut); err != nil {
		fmt.Println("write wav:", err)
	}
	f.Close()
	fmt.Println("WAV written:", *out)
}

// rateCheck measures the true sample rate the server delivers, and whether
// AGC vs manual gain changes the level — this server build (big-endian
// header) ignores some commands, so nothing can be assumed.
func rateCheck(c *rtltcp.Client, gain int) {
	probe := func(label string) {
		buf := make([]byte, 256*1024)
		var total int64
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			c.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := c.ReadIQ(buf)
			total += int64(n)
			if err != nil {
				break
			}
		}
		fmt.Printf("  %-22s %8.0f kB/s → %9.0f samples/s\n", label,
			float64(total)/4/1024, float64(total)/4/2)
	}

	_ = c.SetFrequency(145_100_000)
	_ = c.SetSampleRate(960_000)
	drain(c, 500*time.Millisecond)
	probe("after SetRate(960k)")
	_ = c.SetSampleRate(1_024_000)
	drain(c, 500*time.Millisecond)
	probe("after SetRate(1024k)")

	// Level check at one frequency: AGC vs manual gain, with a byte
	// histogram to expose rail-saturation and DC offset.
	_ = c.SetFrequency(145_100_000)
	level := func() float64 {
		buf := make([]byte, 65536)
		c.ReadIQFull(buf)
		c.ReadIQFull(buf)
		var p float64
		var hist [256]int
		var sumI, sumQ float64
		for j := 0; j+1 < len(buf); j += 2 {
			iv := (float64(buf[j]) - 127.5) / 127.5
			qv := (float64(buf[j+1]) - 127.5) / 127.5
			p += iv*iv + qv*qv
			sumI += iv
			sumQ += qv
			hist[buf[j]]++
			hist[buf[j+1]]++
		}
		n := float64(len(buf))
		db := 10 * math.Log10(p/n+1e-12)
		railPct := 100 * float64(hist[0]+hist[255]) / n
		fmt.Printf("    DC(I)=%+.3f DC(Q)=%+.3f  rail(00/FF)=%.1f%%  min/max used=%d/%d\n",
			sumI/(n/2), sumQ/(n/2), railPct, firstNonZero(hist[:]), lastNonZero(hist[:]))
		return db
	}
	_ = c.SetTunerAGC(true)
	drain(c, 300*time.Millisecond)
	fmt.Printf("  level AGC on:  %.1f dBFS\n", level())
	_ = c.SetTunerAGC(false)
	_ = c.SetGainByIndex(0)
	drain(c, 300*time.Millisecond)
	fmt.Printf("  level gain 0:  %.1f dBFS\n", level())
	_ = c.SetGainByIndex(28)
	drain(c, 300*time.Millisecond)
	fmt.Printf("  level gain 28: %.1f dBFS\n", level())
}

func firstNonZero(h []int) int {
	for i, v := range h {
		if v > 0 {
			return i
		}
	}
	return -1
}

func lastNonZero(h []int) int {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i] > 0 {
			return i
		}
	}
	return -1
}

// ifSpectrum finds where the energy actually sits inside the captured IQ:
// prints the strongest spectral peaks with their offsets from the tuned
// center, so an off-center channel can be re-tuned precisely.
func ifSpectrum(raw []byte) {
	const nfft = 8192
	// Average several windows so a burst or pilot doesn't dominate.
	const windows = 16
	acc := make([]float64, nfft)
	re := make([]float64, nfft)
	im := make([]float64, nfft)
	need := nfft * 2 * windows
	if len(raw) < nfft*2 {
		fmt.Println("  (not enough IQ for spectrum)")
		return
	}
	startAt := len(raw) - need
	for w := 0; w < windows; w++ {
		off := startAt + w*nfft*2
		for i := 0; i < nfft; i++ {
			re[i] = (float64(raw[off+2*i]) - 127.5) / 127.5
			im[i] = (float64(raw[off+2*i+1]) - 127.5) / 127.5
		}
		dsp.HannWindow(re, im)
		dsp.FFT(re, im)
		for i := 0; i < nfft; i++ {
			acc[i] += math.Hypot(re[i], im[i])
		}
	}
	// Power spectrum with bin offsets; average noise = median.
	binHz := float64(dsp.IQRate) / float64(nfft)
	type pk struct {
		off float64
		db  float64
	}
	var vals []float64
	mags := make([]float64, nfft)
	for i := range acc {
		mags[i] = 20 * math.Log10(acc[i]/windows+1e-12)
		vals = append(vals, mags[i])
	}
	sort.Float64s(vals)
	median := vals[len(vals)/2]
	var peaks []pk
	for i := 0; i < nfft; i++ {
		if i > 0 && i < nfft-1 && mags[i] > mags[i-1] && mags[i] >= mags[i+1] && mags[i]-median > 10 {
			off := float64(i)*binHz - float64(dsp.IQRate)/2 // fftshifted offset
			peaks = append(peaks, pk{off, mags[i] - median})
		}
	}
	sort.Slice(peaks, func(a, b int) bool { return peaks[a].db > peaks[b].db })
	fmt.Println("IF spectrum peaks (offset from tuned center):")
	for i, p := range peaks {
		if i >= 6 {
			break
		}
		fmt.Printf("  %+8.1f kHz  %+.1f dB over noise\n", p.off/1e3, p.db)
	}
}

// drain reads and discards IQ for d so the tuner settles.
func drain(c *rtltcp.Client, d time.Duration) {
	buf := make([]byte, 128*1024)
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.SetReadDeadline(deadline)
		c.ReadIQ(buf)
	}
}

type station struct {
	freq  int64
	power float64
}

// scanBand tunes across [lo,hi] in steps, measuring the FFT spectral
// contrast (peak bin over median) at each spot — total power barely moves
// when a 200 kHz station sits inside a 960 kHz window, but the spectrum
// shows it clearly.
func scanBand(c *rtltcp.Client, lo, hi, step int64) []station {
	type hit struct {
		freq  int64
		power float64
	}
	var hits []hit
	fmt.Println("scanning 87.5–108 MHz (FFT spectral contrast) …")
	const nfft = 4096
	re := make([]float64, nfft)
	im := make([]float64, nfft)
	bins := make([]float64, nfft/2)
	buf := make([]byte, nfft*2*4)
	for f := lo; f <= hi; f += step {
		if err := c.SetFrequency(uint32(f)); err != nil {
			fmt.Println("tune failed:", err)
			return nil
		}
		drain(c, 120*time.Millisecond)
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if err := c.ReadIQFull(buf); err != nil {
			continue
		}
		// Center on the last nfft pairs (skips the tune transient).
		off := len(buf) - nfft*2
		for i := 0; i < nfft; i++ {
			re[i] = (float64(buf[off+2*i]) - 127.5) / 127.5
			im[i] = (float64(buf[off+2*i+1]) - 127.5) / 127.5
		}
		dsp.HannWindow(re, im)
		dsp.FFT(re, im)
		for i := range bins {
			// fftshift so bins[0] is the lowest frequency
			bins[i] = 20 * math.Log10(math.Hypot(re[(i+nfft/2)%nfft], im[(i+nfft/2)%nfft])+1e-12)
		}
		sorted := append([]float64(nil), bins...)
		sort.Float64s(sorted)
		median := sorted[len(sorted)/2]
		peak := sorted[len(sorted)-8] // robust peak: 8th loudest bin
		contrast := peak - median
		hits = append(hits, hit{f, contrast})
		bar := int(contrast * 2)
		if bar < 0 {
			bar = 0
		}
		if bar > 40 {
			bar = 40
		}
		fmt.Printf("  %.1f MHz  peak/median %5.1f dB  %s\n", float64(f)/1e6, contrast, strings("█", bar))
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].power > hits[j].power })
	var stations []station
	for _, h := range hits[:6] {
		fmt.Printf("  → candidate %.1f MHz (+%.1f dB over floor)\n", float64(h.freq)/1e6, h.power)
		stations = append(stations, station{h.freq, h.power})
	}
	return stations
}

func strings(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// analyzeAudio prints RMS/peak and — for WFM — hunts the 19 kHz stereo
// pilot, the telltale of a correctly demodulated broadcast signal.
func analyzeAudio(samples []float32, mode dsp.Mode) {
	var sumSq, peak float64
	silence := 0
	for _, s := range samples {
		v := float64(s)
		sumSq += v * v
		if math.Abs(v) > peak {
			peak = math.Abs(v)
		}
		if math.Abs(v) < 1e-4 {
			silence++
		}
	}
	rms := math.Sqrt(sumSq / float64(len(samples)))
	fmt.Printf("audio: %d samples (%.1fs) RMS=%.4f (%.1f dBFS) peak=%.3f silent=%.1f%%\n",
		len(samples), float64(len(samples))/float64(dsp.AudioRate), rms, 20*math.Log10(rms+1e-12), peak,
		100*float64(silence)/float64(len(samples)))

	// FFT the whole capture for spectral peaks.
	n := 1
	for n*2 <= len(samples) {
		n *= 2
	}
	re := make([]float64, n)
	im := make([]float64, n)
	for i := 0; i < n; i++ {
		re[i] = float64(samples[i])
	}
	dsp.HannWindow(re, im)
	dsp.FFT(re, im)
	binHz := float64(dsp.AudioRate) / float64(n)
	power := func(loF, hiF float64) (maxHz, maxDb, medDb float64) {
		lo := int(loF / binHz)
		hi := int(hiF / binHz)
		if hi >= n/2 {
			hi = n/2 - 1
		}
		var vals []float64
		for i := lo; i <= hi; i++ {
			db := 20 * math.Log10(math.Hypot(re[i], im[i])+1e-12)
			vals = append(vals, db)
			if db > maxDb || len(vals) == 1 {
				maxDb, maxHz = db, float64(i)*binHz
			}
		}
		sort.Float64s(vals)
		if len(vals) > 0 {
			medDb = vals[len(vals)/2]
		}
		return
	}

	fmt.Println("top spectral regions:")
	maxHz, maxDb, _ := power(300, 4000)
	fmt.Printf("  voice band 300–4k:  peak %.0f Hz at %.1f dB\n", maxHz, maxDb)
	if mode.Name == "WFM" && dsp.AudioRate >= 44_100 {
		// The 19 kHz pilot check needs audio bandwidth above 19 kHz
		// (not available at the 1.024M capture rate).
		pHz, pDb, pMed := power(18_700, 19_300)
		_, _, ref := power(16_000, 18_500)
		fmt.Printf("  pilot 18.7–19.3k:   peak %.1f Hz, %.1f dB over local median (ref %.1f dB)\n",
			pHz, pDb-pMed, ref)
		if pDb-pMed > 8 && pDb-ref > 6 {
			fmt.Println("  → 19 kHz STEREO PILOT DETECTED — this is real demodulated broadcast FM ✔")
		} else {
			fmt.Println("  → pilot not detected (mono station, weak signal, or off-frequency)")
		}
	}
}
