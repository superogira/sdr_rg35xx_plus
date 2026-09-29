// adsbtest — live ADS-B decode check: connects to an rtl_tcp server
// tuned at 1090 MHz, feeds raw IQ to the Mode S demodulator and
// reports decoded aircraft.
//
// Usage: go run ./cmd/adsbtest [host] [seconds]
package main

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"sdr35/internal/adsb"
	"sdr35/internal/rtltcp"
)

func main() {
	host := "192.168.2.151:2255"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	secs := 30
	if len(os.Args) > 2 {
		if v, err := strconv.Atoi(os.Args[2]); err == nil {
			secs = v
		}
	}
	rate := 2_400_000
	if len(os.Args) > 3 {
		if v, err := strconv.Atoi(os.Args[3]); err == nil {
			rate = v
		}
	}

	c, err := rtltcp.Dial(host, 5*time.Second)
	if err != nil {
		fmt.Println("dial:", err)
		os.Exit(1)
	}
	defer c.CloseGraceful()
	fmt.Printf("connected %s (tuner %d, bigEndian=%v)\n", host, c.Info.TunerType, c.BigEndian())

	// This server family wants SetSampleRate as the connection's FIRST
	// command (e25wop quirk) — anything before it wedges the stream.
	if err := c.SetSampleRate(uint32(rate)); err != nil {
		fmt.Println("rate:", err)
	}
	if err := c.SetFreqCorrection(0); err != nil {
		fmt.Println("kick:", err)
	}
	if err := c.SetFrequency(1_090_000_000); err != nil {
		fmt.Println("freq:", err)
	}
	gain := 300
	if v := os.Getenv("ADSB_GAIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			gain = n
		}
	}
	c.SetTunerAGC(false)
	c.SetGainTenthsDB(int32(gain))

	store := adsb.NewStore()

	buf := make([]byte, 131072)
	// Measure the REAL stream rate first (this server ran 2.388 MSPS
	// when asked for 2.4 — a 0.5% error that walks the bit sampler a
	// full chip by the end of a 112 µs message), then build the demod
	// with the measured value and replay the buffered samples.
	var pre []byte
	t0 := time.Now()
	for time.Since(t0) < 2*time.Second {
		c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := c.ReadIQ(buf)
		if n > 0 {
			pre = append(pre, buf[:n]...)
		}
		if err != nil {
			break
		}
	}
	realRate := float64(len(pre)) / 2 / 2.0
	fmt.Println("measured rate:", int(realRate), "Hz (requested", rate, ")")
	demod := adsb.NewModeSDemod(realRate, store)
	demod.FeedIQ(pre)

	// Quick spectrum sanity: average magnitude of the first 2 s to see
	// whether ANY signal energy is arriving at 1090.
	go func() {
		var sum float64
		var n int
		maxV := 0.0
		t0 := time.Now()
		for time.Since(t0) < 3*time.Second {
			b := make([]byte, 32768)
			m, err := c.ReadIQ(b)
			if err != nil {
				return
			}
			for i := 0; i+1 < m; i += 2 {
				re := float64(b[i]) - 127.5
				im := float64(b[i+1]) - 127.5
				v := re*re + im*im
				sum += v
				if v > maxV {
					maxV = v
				}
				n++
			}
		}
		fmt.Printf("noise rms=%.1f peak=%.1f ratio=%.1f (want peak/noise > 8 for ADS-B)\n",
			math.Sqrt(sum/float64(n)), math.Sqrt(maxV), math.Sqrt(maxV)/math.Sqrt(sum/float64(n)))
	}()
	time.Sleep(3*time.Second + 100*time.Millisecond)
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(deadline) {
		c.SetReadDeadline(time.Now().Add(6 * time.Second))
		n, err := c.ReadIQ(buf)
		if n > 0 {
			demod.FeedIQ(buf[:n])
		}
		if err != nil {
			fmt.Println("read:", err)
			break
		}
		if int(time.Until(deadline).Seconds())%10 == 0 {
			fmt.Printf("t+%2.0fs: %d live", float64(secs)-time.Until(deadline).Seconds(), store.CountLive())
			fmt.Println()
		}
	}
	fmt.Println("demod: preambles=" + strconv.Itoa(demod.Preambles) + " crcFails=" + strconv.Itoa(demod.CrcFails) + " decoded=" + strconv.Itoa(demod.Decoded))
	fmt.Println("--- aircraft ---")
	for _, p := range store.Planes() {
		fmt.Printf("%s  %6s  alt=%6d  spd=%4d  trk=%3d  pos=%v\n",
			p.ICAO, p.Callsign, p.AltFt, p.SpeedKt, p.TrackDeg, p.HasPos)
		if p.HasPos {
			fmt.Printf("        lat=%.5f lon=%.5f\n", p.Lat, p.Lon)
		}
	}
	if len(store.Planes()) == 0 {
		fmt.Println("(none decoded)")
	}
}
