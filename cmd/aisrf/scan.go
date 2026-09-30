package main

import (
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"sdr35/internal/ais"
	"sdr35/internal/dsp"
)

// scanMode sweeps the channel A NCO around -25 kHz to find where the
// real AIS signal actually sits (dongle ppm offset measurement).
func scanMode(host string) {
	c, err := net.Dial("tcp", host)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	hdr := make([]byte, 12)
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	c.Read(hdr)
	c.SetReadDeadline(time.Time{})
	send := func(cmd byte, v uint32) {
		b := []byte{cmd, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
		c.Write(b)
	}
	send(0x01, 162000000)

	buf := make([]byte, 65536)
	chTaps := dsp.DesignLowpass(127, 16000, 256000)
	var hist []complex128
	nco := 0.0
	// collect ~4 s of IQ at channel A
	var collected []complex128
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if err != nil {
			break
		}
		for i := 0; i+1 < n; i += 2 {
			collected = append(collected, complex(float64(buf[i])-127.5, float64(buf[i+1])-127.5))
		}
	}
	fmt.Println("captured", len(collected), "samples")
	// Coarse spectrum via Goertzel; print dB relative to the median bin
	// so the channel peak stands out regardless of absolute level.
	var offs []float64
	var pws []float64
	for off := -35000.0; off <= -15000.0; off += 250 {
		offs = append(offs, off)
		pws = append(pws, goertzelPower(collected, off))
	}
	sortedP := append([]float64{}, pws...)
	for x := 1; x < len(sortedP); x++ {
		for y := x; y > 0 && sortedP[y] < sortedP[y-1]; y-- {
			sortedP[y], sortedP[y-1] = sortedP[y-1], sortedP[y]
		}
	}
	med := sortedP[len(sortedP)/2]
	for k2, off := range offs {
		db := 10 * math.Log10(pws[k2]/med)
		if db > 3 {
			fmt.Printf("%7.0f Hz: %+.1f dB"+string(rune(92))+"n", off, db)
		}
	}
	// Centroid per AIS channel window (same recipe as the app's AFC).
	full := make([]float64, 0, 512)
	bins := make([]float64, 0, 512)
	for bin := 1; bin < 1024; bin++ {
		f := float64(bin) * (128000.0 / 1024.0)
		if f > 64000 {
			f -= 128000
		}
		full = append(full, f)
		bins = append(bins, 0) // placeholder
	}
	_ = bins
	for _, base := range []float64{-25000, 25000} {
		var sp, sfp float64
		for _, f := range full {
			if math.Abs(f-base) > 8000 {
				continue
			}
			bin := int(math.Round(f / (128000.0 / 1024.0)))
			if bin < 0 {
				bin += 1024
			}
			p := goertzelPower(collected, f)
			sp += p
			sfp += f * p
		}
		if sp > 0 {
			fmt.Printf("centroid base %+.0f: centre = %+.0f Hz (offset %+.0f)"+string(rune(92))+"n", base, sfp/sp, sfp/sp-base)
		}
	}
	_ = chTaps
	_ = hist
	_ = nco
	_ = math.Pi
	_ = dsp.FIRDecim
	_ = ais.NewStore
}

func goertzelPower(x []complex128, f float64) float64 {
	k := 2 * math.Pi * f / 256000
	coeff := 2 * math.Cos(k)
	var s1r, s2r, s1i, s2i float64
	for _, z := range x {
		sr := real(z) + coeff*s1r - s2r
		si := imag(z) + coeff*s1i - s2i
		s2r, s1r = s1r, sr
		s2i, s1i = s1i, si
	}
	return (s1r*s1r + s2r*s2r - coeff*s1r*s2r) + (s1i*s1i + s2i*s2i - coeff*s1i*s2i)
}
