// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"math"
	"os"
	"strings"

	"sdr35/internal/ais"
	"sdr35/internal/dsp"
)

// fileMode replays a recorded raw CU8 file through the same
// front-end + demodulator as the live harness.
var known [][]byte

func loadKnown(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' || r == '\r' }) {
		f := strings.Split(line, ",")
		if len(f) < 6 {
			continue
		}
		var bits []byte
		for k := 0; k < len(f[5]); k++ {
			c := f[5][k]
			if c < '0' || c > 'z' {
				continue
			}
			v := c - 48
			if v > 40 {
				v -= 8
			}
			for b := 5; b >= 0; b-- {
				bits = append(bits, (v>>uint(b))&1)
			}
		}
		if len(bits) >= 168 {
			known = append(known, bits)
		}
	}
}

func fileMode(path string, _ int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("read:", err)
		os.Exit(1)
	}
	fmt.Println("file bytes:", len(data))
	loadKnown(os.Getenv("AIS_KNOWN"))
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
		fmt.Printf("ch%s %5.1fdB type %2d MMSI %s%s\n", name, levelDb, typ, mmsi, extra)
	}
	demA := ais.NewChannelDemod(48000, 0, "A", onPayload)
	demB := ais.NewChannelDemod(48000, 1, "B", onPayload)
	// Recording is at ~1.024 Msps: pre-decimate by 4 to the 256 ks/s
	// the live front-end expects (wide lowpass keeps both channels).
	preTaps := dsp.DesignLowpass(255, 100000, 1024000)
	var preHist []complex128
	chTaps := dsp.DesignLowpass(127, 16000, 256000)
	var histA, histB []complex128
	nco := [2]float64{}
	posA, posB := 0.0, 0.0
	afc := [2]float64{}
	const CH = 65536
	for off := 0; off+CH <= len(data); off += CH {
		chunk := data[off : off+CH]
		full := make([]complex128, 0, CH/2)
		for i := 0; i+1 < len(chunk); i += 2 {
			full = append(full, complex(float64(chunk[i])-127.5, float64(chunk[i+1])-127.5))
		}
		var fif2 []complex128
		dsp.FIRDecim(preTaps, &preHist, 4, full, &fif2)
		afcCollect(fif2)
		for ch := 0; ch < 2; ch++ {
			f := -25000.0 + afc[0]
			hist := &histA
			dem := demA
			pos := &posA
			if ch == 1 {
				f = 25000.0 + afc[1]
				hist, dem, pos = &histB, demB, &posB
			}
			incr := -2 * 3.141592653589793 * f / 256000
			rot := make([]complex128, len(fif2))
			for i, z := range fif2 {
				w := nco[ch]
				cw, sw := mathCos(w), mathSin(w)
				rot[i] = complex(real(z)*cw-imag(z)*sw, real(z)*sw+imag(z)*cw)
				nco[ch] += incr
				if nco[ch] > 2*3.141592653589793 {
					nco[ch] -= 2 * 3.141592653589793
				} else if nco[ch] < -2*3.141592653589793 {
					nco[ch] += 2 * 3.141592653589793
				}
			}
			var dec, out48 []complex128
			dsp.FIRDecim(chTaps, hist, 4, rot, &dec)
			dsp.ResampleLinear(dec, pos, 0.75, &out48)
			dem.Feed(out48)
		}
		afc[0], afc[1] = afcOff[0], afcOff[1]
	}
	fmt.Println("frames:", frames)
	// Ground truth comparison: known payload bit streams (AIS-catcher
	// decoded this same file) vs every failed FCS attempt.
	if len(known) > 0 && len(ais.DbgAttempts) > 0 {
		hit, direct, inv, bestM := 0, 0, 0, 0
		for _, att := range ais.DbgAttempts {
			for _, kb := range known {
				n := len(kb)
				if n > len(att) {
					n = len(att)
				}
				if n < 120 {
					continue
				}
				m, iv := 0, 0
				for j := 0; j < n; j++ {
					if att[j] == kb[j] {
						m++
					} else {
						iv++
					}
				}
				if m > bestM {
					bestM = m
				}
				if iv > bestM {
					bestM = iv
				}
				if m*100/n > 90 {
					hit++
					direct++
					break
				}
				if iv*100/n > 90 {
					hit++
					inv++
					break
				}
			}
		}
		fmt.Printf("attempts=%d matching known payloads: %d (direct=%d inverted=%d) best=%d/168\n", len(ais.DbgAttempts), hit, direct, inv, bestM)
	}
	fmt.Printf("afc: A=%+.0f B=%+.0f\n", afcOff[0], afcOff[1])
}

func mathCos(x float64) float64 { return math.Cos(x) }
func mathSin(x float64) float64 { return math.Sin(x) }
