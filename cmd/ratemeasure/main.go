// ratemeasure: stream-rate and gap profiler for an rtl_tcp source.
// Usage: ratemeasure <rate> <host:port> [seconds]
// Prints the average rate, per-read gap histogram (the dropouts that
// shift FT8's audio timeline) and the loss against the nominal rate.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"sdr35/internal/rtltcp"
)

func main() {
	rate := 1024000
	host := "192.168.2.151:2255"
	secs := 30
	if len(os.Args) > 1 {
		fmt.Sscanf(os.Args[1], "%d", &rate)
	}
	if len(os.Args) > 2 {
		host = os.Args[2]
	}
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &secs)
	}
	c, err := rtltcp.Dial(host, 5*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer c.CloseGraceful()
	c.SetSampleRate(uint32(rate))
	c.SetFrequency(21_074_000)
	buf := make([]byte, 65536)
	var total int
	var gaps [5]int // <10ms, <50, <200, <1s, >=1s
	worst := time.Duration(0)
	prev := time.Now()
	t0 := prev
	for time.Since(t0) < time.Duration(secs)*time.Second {
		c.SetReadDeadline(time.Now().Add(4 * time.Second))
		n, err := c.ReadIQ(buf)
		now := time.Now()
		if err != nil {
			fmt.Println("read:", err)
			break
		}
		total += n
		g := now.Sub(prev)
		prev = now
		switch {
		case g < 10*time.Millisecond:
			gaps[0]++
		case g < 50*time.Millisecond:
			gaps[1]++
		case g < 200*time.Millisecond:
			gaps[2]++
		case g < time.Second:
			gaps[3]++
		default:
			gaps[4]++
		}
		if g > worst {
			worst = g
		}
	}
	dt := time.Since(t0).Seconds()
	meas := float64(total) / dt / 2
	fmt.Printf("host=%s nominal=%d measured=%.0f sps (%.2f%% of nominal)\n", host, rate, meas, 100*meas/float64(rate))
	fmt.Printf("reads: <10ms=%d <50ms=%d <200ms=%d <1s=%d >=1s=%d  worst gap=%v\n", gaps[0], gaps[1], gaps[2], gaps[3], gaps[4], worst.Round(time.Millisecond))
	_ = strconv.Itoa(0)
}
