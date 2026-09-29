// adsbscope — dumps the magnitude envelope around the strongest pulse
// from a live 1090 MHz rtl_tcp stream, to eyeball what the Mode S
// signal (or its absence) looks like.
package main

import (
	"fmt"
	"math"
	"os"
	"time"

	"sdr35/internal/rtltcp"
)

func main() {
	host := "192.168.2.151:2255"
	rate := 2_400_000
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &rate)
	}

	c, err := rtltcp.Dial(host, 5*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.CloseGraceful()
	if err := c.SetSampleRate(uint32(rate)); err != nil {
		fmt.Println("rate:", err)
	}
	c.SetFreqCorrection(0)
	c.SetFrequency(1_090_000_000)
	c.SetTunerAGC(true)

	buf := make([]byte, 131072)
	var win []float64
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && len(win) < 600000 {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := c.ReadIQ(buf)
		if err != nil {
			break
		}
		for i := 0; i+1 < n; i += 2 {
			re := float64(buf[i]) - 127.5
			im := float64(buf[i+1]) - 127.5
			win = append(win, math.Sqrt(re*re+im*im))
		}
	}
	fmt.Printf("%d samples, ", len(win))

	// Strongest 1 µs (rate/1e6 samples) window → its start index.
	w := rate / 1e6
	best, bestI := 0.0, 0
	for i := 0; i+w < len(win); i += w / 2 {
		sum := 0.0
		for k := 0; k < w; k++ {
			sum += win[i+k]
		}
		if sum > best {
			best, bestI = sum, i
		}
	}
	fmt.Printf("peak window at %d\n", bestI)
	lo := bestI - 3*w
	if lo < 0 {
		lo = 0
	}
	hi := bestI + 40*w
	if hi > len(win) {
		hi = len(win)
	}
	for i := lo; i < hi; i++ {
		v := int(win[i] / 4)
		if v > 45 {
			v = 45
		}
		fmt.Printf("%5d %5.0f %s\n", i, win[i], string(rune('a'+v/2)))
	}
}
