package main

import (
	"fmt"
	"math"
	"os"

	"sdr35/internal/ais"
	"sdr35/internal/dsp"
)

// burstMode finds the strongest channel-A bursts in a recording, tunes
// the NCO exactly per burst, and runs the full demod on each one —
// isolating classifier fidelity from AFC/FLL acquisition issues.
func burstMode(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("read:", err)
		os.Exit(1)
	}
	// complex IQ at 1.024M
	n := len(data) / 2
	// channel A power per 8192-sample block via Goertzel around -22.9k
	type blk struct {
		off int
		p   float64
	}
	var blocks []blk
	for off := 0; off+16384 <= n; off += 8192 {
		var s1r, s2r, s1i, s2i float64
		coeff := 2 * math.Cos(2*math.Pi*22900/1024000)
		for i := off; i < off+8192; i++ {
			re := float64(data[2*i]) - 127.5
			im := float64(data[2*i+1]) - 127.5
			sr := re + coeff*s1r - s2r
			si := im + coeff*s1i - s2i
			s2r, s1r = s1r, sr
			s2i, s1i = s1i, si
		}
		p := (s1r*s1r + s2r*s2r - coeff*s1r*s2r) + (s1i*s1i + s2i*s2i - coeff*s1i*s2i)
		blocks = append(blocks, blk{off, p})
	}
	// top blocks, at least 8192 samples apart
	ranked := append([]blk{}, blocks...)
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].p > ranked[j-1].p; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	var top []blk
	for _, b := range ranked {
		ok := true
		for _, t := range top {
			if abs(t.off-b.off) < 16384 {
				ok = false
				break
			}
		}
		if ok {
			top = append(top, b)
		}
		if len(top) == 10 {
			break
		}
	}
	preTaps := dsp.DesignLowpass(255, 100000, 1024000)
	chTaps := dsp.DesignLowpass(127, 16000, 256000)
	for k, b := range top {
		// 40 ms window around the burst
		lo := b.off - 40960
		if lo < 0 {
			lo = 0
		}
		hi := b.off + 81920
		if hi > n {
			hi = n
		}
		var full []complex128
		for i := lo; i < hi; i++ {
			full = append(full, complex(float64(data[2*i])-127.5, float64(data[2*i+1])-127.5))
		}
		// Channel centre = energy centroid over the -25k±8k window
		// (the max-hold bin of a broad GMSK lobe is NOT the centre).
		var sp, sfp float64
		for f := -33000.0; f <= -17000.0; f += 125.0 {
			p := goertzelPower(full, f)
			sp += p
			sfp += f * p
		}
		bestF := sfp / sp
		// demod with the exact NCO
		got := 0
		var text []string
		dem := ais.NewChannelDemod(48000, 0, "A", func(p []byte, ch int) {
			got++
			st := ais.NewStore()
			typ, mmsi := st.DecodeBits(p)
			text = append(text, fmt.Sprintf("type %d MMSI %s", typ, mmsi))
		})
		var preHist, hist []complex128
		nco := 0.0
		incr := -2 * math.Pi * bestF / 256000
		var dec, out48 []complex128
		pos := 0.0
		var d4 []complex128
		dsp.FIRDecim(preTaps, &preHist, 4, full, &d4)
		for _, z := range d4 {
			w := nco
			cw, sw := math.Cos(w), math.Sin(w)
			r := complex(real(z)*cw-imag(z)*sw, real(z)*sw+imag(z)*cw)
			nco += incr
			if nco > 2*math.Pi {
				nco -= 2 * math.Pi
			} else if nco < -2*math.Pi {
				nco += 2 * math.Pi
			}
			dec = append(dec, r)
		}
		dsp.FIRDecim(chTaps, &hist, 4, dec, &out48)
		var r48 []complex128
		dsp.ResampleLinear(out48, &pos, 0.75, &r48)
		dem.Feed(r48)
		fmt.Printf("burst %d: f=%+.0f frames=%d %v"+string(rune(92))+"n", k, bestF, got, text)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
