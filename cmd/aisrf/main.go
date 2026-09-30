package main

import (
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"time"

	"sdr35/internal/ais"
	"sdr35/internal/dsp"
)

// Live AIS RF harness: connect an rtl_tcp server, tune 162.000 MHz,
// run the same IF2 + per-channel demod path the app uses, print frames.
func main() {
	host := "192.168.2.151:2255"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	secs := 60
	if len(os.Args) > 2 {
		secs, _ = strconv.Atoi(os.Args[2])
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
	// leave the rate alone and adapt the DSP below. Just retune.
	send(0x01, 162000000)
	_ = send
	fmt.Println("streaming", secs, "s from", host)

	frames := 0
	onPayload := func(p []byte, ch int) {
		s := ais.NewStore()
		typ, mmsi := s.DecodeBits(p)
		name := "A"
		if ch == 1 {
			name = "B"
		}
		fmt.Printf("%s ch%s type %d MMSI %s (%d bytes)\n", time.Now().Format("15:04:05"), name, typ, mmsi, len(p))
		frames++
	}
	demA := ais.NewChannelDemod(64000, 0, "A", onPayload)
	demB := ais.NewChannelDemod(64000, 1, "B", onPayload)

	// Server runs 256 ksps natively: use it as the IF2 stream directly.
	chTaps := dsp.DesignLowpass(127, 16000, 256000)
	var histA, histB []complex128
	nco := [2]float64{}
	prevArg := [2]float64{}
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	buf := make([]byte, 32768)
	scratch := make([]complex128, 0, 16384)
	var fif2 []complex128
	t0, bytesTotal := time.Now(), 0
	var pMaxA, pSumA float64
	var pNA int
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if err != nil {
			fmt.Println("read:", err)
			break
		}
		bytesTotal += n
		if el := time.Since(t0); el > 5*time.Second {
			fmt.Printf("rate: %.3f MB/s (%.3f Msps IQ)\n", float64(bytesTotal)/el.Seconds()/1e6, float64(bytesTotal)/el.Seconds()/2e6)

			t0, bytesTotal = time.Now(), 0
		}
		iq := buf[:n]
		scratch = scratch[:0]
		for i := 0; i+1 < len(iq); i += 2 {
			scratch = append(scratch, complex(float64(iq[i])-127.5, float64(iq[i+1])-127.5))
		}
		fif2 = scratch
		for ch := 0; ch < 2; ch++ {
			f := -25000.0
			dem := demA
			hist := &histA
			if ch == 1 {
				f, dem, hist = 25000.0, demB, &histB
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
			var dec []complex128
			dsp.FIRDecim(chTaps, hist, 4, rot, &dec)
			var fm, pow []float64
			for _, z := range dec {
				arg := math.Atan2(imag(z), real(z))
				delta := arg - prevArg[ch]
				if delta > math.Pi {
					delta -= 2 * math.Pi
				} else if delta < -math.Pi {
					delta += 2 * math.Pi
				}
				fm = append(fm, delta)
				pow = append(pow, real(z)*real(z)+imag(z)*imag(z))
				prevArg[ch] = arg
			}
			if ch == 0 {
				for _, p := range pow {
					if p > pMaxA {
						pMaxA = p
					}
					pSumA += p
					pNA++
				}
				if pNA >= 64*64000 {
					fmt.Printf("chA power: mean=%.4f max=%.4f\n", pSumA/float64(pNA), pMaxA)

					pMaxA, pSumA, pNA = 0, 0, 0
				}
			}
			dem.Feed(fm, pow)
		}
	}
	fmt.Println("frames:", frames)
}
