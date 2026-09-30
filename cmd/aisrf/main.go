package main

import (
	"fmt"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"time"

	"sdr35/internal/ais"
	"sdr35/internal/dsp"
)

// Live AIS RF harness: connect an rtl_tcp server, tune 162.000 MHz,
// run the same front-end the app uses (NCO ±25k → FIR → 48k) into the
// real ChannelDemod, and print decoded frames.
var (
	afcBuf []complex128
	afcOff [2]float64
)

// afcFeed accumulates every-2nd samples; once a 2048-sample snapshot
// is complete it locates each channel's spectral centroid near ±25 kHz
// and slews the NCO offsets.
func afcCollect(in []complex128) {
	for i := 0; i < len(in); i += 2 {
		afcBuf = append(afcBuf, in[i])
		if len(afcBuf) == 2048 {
			afcRun()
			afcBuf = afcBuf[:0]
		}
	}
}

func afcRun() {
	n := 1024
	re := make([]float64, n)
	im := make([]float64, n)
	for i := 0; i < n; i++ {
		z := afcBuf[i*2]
		re[i], im[i] = real(z), imag(z)
	}
	dsp.FFT(re, im)
	const rate = 128000.0
	for ch := 0; ch < 2; ch++ {
		base := -25000.0
		if ch == 1 {
			base = 25000.0
		}
		var sp, sfp float64
		var win []float64
		for off := -8000.0; off <= 8000.0; off += 125.0 {
			f := base + off
			bin := int(math.Round(f / (rate / float64(n))))
			if bin < 0 {
				bin += n
			}
			if bin < 1 || bin >= n {
				continue
			}
			p := re[bin]*re[bin] + im[bin]*im[bin]
			win = append(win, p)
			sp += p
			sfp += f * p
		}
		if sp <= 0 {
			continue
		}
		sort.Float64s(win)
		med := win[len(win)/2]
		if med > 0 && sp > med*float64(len(win))*2.5 {
			afcOff[ch] += 0.15 * (sfp/sp - base - afcOff[ch])
			if afcOff[ch] > 8000 {
				afcOff[ch] = 8000
			} else if afcOff[ch] < -8000 {
				afcOff[ch] = -8000
			}
		}
	}
}

func main() {
	host := "192.168.2.151:2255"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	secs := 60
	if len(os.Args) > 2 {
		secs, _ = strconv.Atoi(os.Args[2])
	}
	if os.Getenv("AIS_SCAN") != "" {
		scanMode(host)
		return
	}
	if p := os.Getenv("AIS_REC"); p != "" {
		recordMode(host, p, secs)
		return
	}
	if p := os.Getenv("AIS_FILE"); p != "" {
		fileMode(p, int64(secs))
		return
	}
	if p := os.Getenv("AIS_BURST"); p != "" {
		burstMode(p)
		return
	}
	if p := os.Getenv("AIS_MKSYNTH"); p != "" {
		mkSynth(p)
		return
	}
	if p := os.Getenv("AIS_BURSTS"); p != "" {
		writeBursts(p)
		return
	}
	c, err := net.Dial("tcp", host)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	hdr := make([]byte, 12)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(hdr); err != nil {
		fmt.Println("handshake:", err)
		os.Exit(1)
	}
	c.SetReadDeadline(time.Time{})
	// rtl_tcp frame: 1 command byte + 4 value bytes, big-endian.
	send := func(cmd byte, v uint32) {
		b := []byte{cmd, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		if _, err := c.Write(b); err != nil {
			fmt.Println("send:", err)
			os.Exit(1)
		}
	}
	// The server keeps its own 256 ksps rate (it feeds aiscatcher);
	// leave the rate alone and run the front-end at 256k directly.
	send(0x01, 162000000)
	fmt.Println("streaming", secs, "s from", host)

	// Measure the true stream rate (the shared server's configuration
	// drifts between 256 ksps and ~1.024 Msps) and pre-decimate when
	// needed so the front-end always sees 256 ksps.
	bufRate := make([]byte, 65536)
	t0 := time.Now()
	total := 0
	for time.Since(t0) < 2*time.Second {
		m, err := c.Read(bufRate)
		if err != nil {
			break
		}
		total += m
	}
	iqRate := float64(total) / time.Since(t0).Seconds() / 2
	fmt.Printf("measured IQ rate: %.0f sps\n", iqRate)
	var preTaps []float64
	var preHist []complex128
	if iqRate > 700000 {
		preTaps = dsp.DesignLowpass(255, 100000, 1024000)
	}
	pre := func(in []complex128) []complex128 {
		if preTaps == nil {
			return in
		}
		var out []complex128
		dsp.FIRDecim(preTaps, &preHist, 4, in, &out)
		return out
	}

	store := ais.NewStore()
	frames := 0
	onPayload := func(p []byte, ch int, levelDb float64) {
		frames++
		typ, mmsi := store.DecodeBits(p)
		name := "A"
		if ch == 1 {
			name = "B"
		}
		sh := store.Ship(mmsi)
		extra := ""
		if sh != nil && sh.HasPos {
			extra = fmt.Sprintf(" pos=%.4f,%.4f sog=%.1f", sh.Lat, sh.Lon, sh.SogKt)
		}
		fmt.Printf("%s ch%s type %2d MMSI %s%s\n", time.Now().Format("15:04:05"), name, typ, mmsi, extra)
	}
	// Dongle ppm shifts the channels away from ±25 kHz; allow an
	// explicit NCO offset for live experiments.
	offA := 0.0
	offB := 0.0
	if v := os.Getenv("AIS_OFF_A"); v != "" {
		fmt.Sscanf(v, "%f", &offA)
	}
	if v := os.Getenv("AIS_OFF_B"); v != "" {
		fmt.Sscanf(v, "%f", &offB)
	}
	fmt.Println("NCO offsets:", offA, offB)
	demA := ais.NewChannelDemod(48000, 0, "A", onPayload)
	demB := ais.NewChannelDemod(48000, 1, "B", onPayload)

	chTaps := dsp.DesignLowpass(127, 16000, 256000)
	var histA, histB []complex128
	nco := [2]float64{}
	posA, posB := 0.0, 0.0
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	buf := make([]byte, 32768)
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if err != nil {
			fmt.Println("read:", err)
			break
		}
		iq := buf[:n]
		full := make([]complex128, 0, n/2)
		for i := 0; i+1 < len(iq); i += 2 {
			full = append(full, complex(float64(iq[i])-127.5, float64(iq[i+1])-127.5))
		}
		fif2 := pre(full)
		// Same per-second centroid AFC as the app's Chain: the shared
		// server's tuning state drifts (aiscatcher retunes it), so the
		// harness must find the channels itself.
		afcCollect(fif2)
		for ch := 0; ch < 2; ch++ {
			f := -25000.0 + offA + afcOff[0]
			hist := &histA
			dem := demA
			pos := &posA
			if ch == 1 {
				f = 25000.0 + offB + afcOff[1]
				hist, dem, pos = &histB, demB, &posB
			}
			incr := -2 * math.Pi * f / 256000
			rot := make([]complex128, len(fif2))
			for i, z := range fif2 {
				w := nco[ch]
				cw, sw := math.Cos(w), math.Sin(w)
				rot[i] = complex(real(z)*cw-imag(z)*sw, real(z)*sw+imag(z)*cw)
				nco[ch] += incr
				if nco[ch] > 2*math.Pi {
					nco[ch] -= 2 * math.Pi
				} else if nco[ch] < -2*math.Pi {
					nco[ch] += 2 * math.Pi
				}
			}
			var dec, out48 []complex128
			dsp.FIRDecim(chTaps, hist, 4, rot, &dec)
			dsp.ResampleLinear(dec, pos, 0.75, &out48)
			dem.Feed(out48)
		}
	}
	fmt.Println("frames:", frames)
}
