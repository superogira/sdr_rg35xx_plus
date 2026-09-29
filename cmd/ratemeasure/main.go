package main

import (
	"fmt"
	"os"
	"time"

	"sdr35/internal/rtltcp"
)

func main() {
	rate := 3200000
	fmt.Sscanf(os.Args[1], "%d", &rate)
	c, err := rtltcp.Dial("192.168.2.151:2255", 5*time.Second)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer c.CloseGraceful()
	c.SetSampleRate(uint32(rate))
	c.SetFreqCorrection(0)
	c.SetFrequency(1_090_000_000)
	c.SetTunerAGC(false)
	c.SetGainTenthsDB(350)
	buf := make([]byte, 65536)
	var total int
	t0 := time.Now()
	for time.Since(t0) < 8*time.Second {
		c.SetReadDeadline(time.Now().Add(4 * time.Second))
		n, err := c.ReadIQ(buf)
		total += n
		if err != nil {
			break
		}
	}
	el := time.Since(t0).Seconds()
	fmt.Printf("requested %d → actual %.3f Msps (%d bytes in %.1fs)\n", rate, float64(total)/2/el/1e6, total, el)
}
