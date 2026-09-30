package main

import (
	"fmt"
	"math"
	"os"

	"sdr35/internal/ais"
	"sdr35/internal/dsp"
)

// fileMode replays a recorded raw CU8 file through the same
// front-end + demodulator as the live harness.
func fileMode(path string, _ int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("read:", err)
		os.Exit(1)
	}
	fmt.Println("file bytes:", len(data))
	store := ais.NewStore()
	frames := 0
	onPayload := func(p []byte, ch int) {
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
		fmt.Printf("ch%s type %2d MMSI %s%s\n", name, typ, mmsi, extra)
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
	fmt.Printf("afc: A=%+.0f B=%+.0f\n", afcOff[0], afcOff[1])
}

func mathCos(x float64) float64 { return math.Cos(x) }
func mathSin(x float64) float64 { return math.Sin(x) }
