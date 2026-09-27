// rttytest — live RTTY decoder check: connects to the rtl_tcp server,
// tunes a frequency, captures IQ, replays it through the app's exact
// USB chain + RTTY monitor, and sweeps the mark tone if the standard
// 2125/2295 pair yields nothing.
//
// Usage: go run ./cmd/rttytest [freqHz] [seconds]
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"sdr35/internal/dsp"
	"sdr35/internal/rtltcp"
)

const host = "e25wop.thddns.net:2255"

func main() {
	freq := int64(21_114_000)
	if len(os.Args) > 1 {
		if v, err := strconv.ParseInt(os.Args[1], 10, 64); err == nil {
			freq = v
		}
	}
	secs := 18
	if len(os.Args) > 2 {
		if v, err := strconv.Atoi(os.Args[2]); err == nil {
			secs = v
		}
	}

	c, err := rtltcp.Dial(host, 5*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.CloseGraceful()
	fmt.Printf("connected %s (tuner %d gains, bigEndian=%v)\n", host, c.Info.TunerType, c.BigEndian())

	// Bring-up: correction kick, DS=0 (see above), tune, manual gain.
	if err := c.SetFreqCorrection(0); err != nil {
		fmt.Println("kick:", err)
	}
	if err := c.SetDirectSampling(0); err != nil {
		fmt.Println("ds:", err)
	}
	if err := c.SetFrequency(uint32(freq)); err != nil {
		fmt.Println("freq:", err)
	}
	c.SetTunerAGC(false)
	c.SetGainTenthsDB(300)

	// Capture raw IQ.
	var iq []byte
	buf := make([]byte, 16384)
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(6 * time.Second))
		n, err := c.ReadIQ(buf)
		if err != nil {
			fmt.Println("read:", err)
			break
		}
		iq = append(iq, buf[:n]...)
	}
	fmt.Printf("captured %.1f s (%d bytes)\n", float64(len(iq))/4/2_048_000, len(iq))

	// Replay through the app's chain with the standard tones first.
	run := func(mark, space float64) (lines []string, cur string, m, s float64) {
		dsp.SetIQRate(2_048_000)
		dec := dsp.NewRTTYDecoder()
		dec.SetEnabled(true)
		dec.SetTones(mark, space)
		ch := dsp.NewChain(dsp.ModeUSB, nil, nil)
		ch.SetRTTYDetector(dec)
		var audio []float32
		for pos := 0; pos < len(iq); pos += 16384 {
			end := pos + 16384
			if end > len(iq) {
				end = len(iq)
			}
			ch.Process(iq[pos:end], &audio)
			audio = audio[:0]
		}
		lines = dec.TakeLines()
		cur = dec.Current()
		m, s = dec.Levels()
		return
	}

	lines, cur, m, s := run(2125, 2295)
	fmt.Printf("standard 2125/2295: mark=%.2f space=%.2f lines=%d\n", m, s, len(lines))
	for _, ln := range lines {
		fmt.Printf("RTTY> %s\n", ln)
	}
	if cur != "" {
		fmt.Printf("RTTY~ %s\n", cur)
	}

	// Sweep the mark tone (threshold centre) regardless — a signal that
	// frames at 45.45 baud with consistently wrong letters means the
	// tone pair sits off-frequency. Try 170/200/450 Hz shifts too.
	for _, shift := range []float64{170, 200, 450} {
		step := 15.0
		span := 225.0
		if shift == 170.0 {
			step, span = 5, 60 // fine sweep for the standard shift
		}
		for off := -span; off <= span; off += step {
			mark := 2125 + float64(off)
			ls, c2, m2, s2 := run(mark, mark+shift)
			if len(ls) == 0 && c2 == "" {
				continue
			}
			fmt.Printf("--- mark=%.0f shift=%.0f (levels %.2f/%.2f)", mark, shift, m2, s2)
			fmt.Println()
			for _, ln := range ls {
				fmt.Println("RTTY> " + ln)
			}
			if c2 != "" {
				fmt.Println("RTTY~ " + c2)
			}
		}
	}
	fmt.Println("sweep done")
}
