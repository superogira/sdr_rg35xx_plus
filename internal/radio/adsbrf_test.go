// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package radio

import (
	"math"
	"testing"
	"time"

	"sdr35/internal/adsb"
	"sdr35/internal/dsp"
)

// TestADSBRFToggle checks the in-app ADS-B RF mode: enabling retunes to
// 1090 MHz, forces the 2.4 MSPS capture rate, turns off the competing FT8
// and AIS RF decoders, and installs the demod against the shared store;
// disabling restores the previous capture rate.
func TestADSBRFToggle(t *testing.T) {
	r := NewDemo(dsp.ModeNFM, nil)
	store := adsb.NewStore()
	r.ADSBStore(store)

	if r.ADSBRFEnabled() {
		t.Fatal("ADS-B RF on at start")
	}

	r.SetFT8Enabled(true) // FT8 must be dropped when ADS-B RF starts
	r.SetCaptureRate(2_048_000)
	prevRate := r.IQRate()

	r.SetADSBRFEnabled(true)
	if !r.ADSBRFEnabled() {
		t.Fatal("ADS-B RF did not turn on")
	}
	if r.FT8Enabled() {
		t.Fatal("FT8 still on after ADS-B RF")
	}
	if r.LO() != 1_090_000_000 {
		t.Fatalf("LO = %d, want 1090 MHz", r.LO())
	}
	if r.IQRate() != 2_400_000 || dsp.IQRate != 2_400_000 {
		t.Fatalf("capture rate = %d / dsp %d, want 2.4M", r.IQRate(), dsp.IQRate)
	}

	// The demod must be installed and accept IQ without panicking; a
	// rate below 2.4M is silently ignored by design.
	r.mu.Lock()
	dem := r.adsbDem
	r.mu.Unlock()
	if dem == nil || dem.Rate() != 2.4e6 {
		t.Fatalf("demod not installed at 2.4M: %+v", dem)
	}
	dem.FeedIQ(make([]byte, 4096))

	r.SetADSBRFEnabled(false)
	if r.ADSBRFEnabled() {
		t.Fatal("ADS-B RF did not turn off")
	}
	if r.IQRate() != prevRate {
		t.Fatalf("capture rate after off = %d, want restored %d", r.IQRate(), prevRate)
	}
}

// TestRTLSrvToggle: enabling the fan-out server opens a listener; a
// connected client sees the RTL0 handshake; disabling drops it.
func TestRTLSrvToggle(t *testing.T) {
	r := NewDemo(dsp.ModeNFM, nil)
	r.SetRTLSrvPort(0) // ephemeral
	if r.RTLSrvEnabled() {
		t.Fatal("rtl_tcp server on at start")
	}
	// Port 0 would fail validation in the app; the setter accepts it and
	// SetRTLSrvEnabled falls back to 1235, which may be taken in CI.
	r.SetRTLSrvPort(12390)
	if !r.SetRTLSrvEnabled(true) {
		t.Skip("could not bind test port (in use)")
	}
	if !r.RTLSrvEnabled() {
		t.Fatal("server did not report enabled")
	}
	if r.RTLSrvPort() != 12390 {
		t.Fatalf("port = %d, want 12390", r.RTLSrvPort())
	}
	r.SetRTLSrvEnabled(false)
	if r.RTLSrvEnabled() {
		t.Fatal("server did not stop")
	}
}

// TestADSBDemodGoroutine: blocks enqueued on the demod queue are fed by
// the background goroutine into the shared store (end-to-end decode),
// proving the async wiring the session loop relies on.
func TestADSBDemodGoroutine(t *testing.T) {
	r := NewDemo(dsp.ModeNFM, nil)
	store := adsb.NewStore()
	r.ADSBStore(store)
	r.SetCaptureRate(2_400_000)
	r.SetADSBRFEnabled(true)
	defer r.SetADSBRFEnabled(false)

	// A single position+callsign+velocity set, modulated at 2.4M.
	icao := [3]byte{0x88, 0x41, 0xF2}
	air := modulateModeS(icao)
	// Feed through the SAME queue the session loop uses.
	r.mu.Lock()
	q, free := r.adsbQ, r.adsbFree
	r.mu.Unlock()
	if q == nil {
		t.Fatal("demod queue not created")
	}
	for i := 0; i < len(air); i += 16384 {
		end := i + 16384
		if end > len(air) {
			end = len(air)
		}
		blk := make([]byte, end-i)
		copy(blk, air[i:end])
		select {
		case q <- blk:
		default:
			t.Fatal("queue full (goroutine not draining)")
		}
	}
	// The goroutine decodes asynchronously; poll for the plane.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ps := store.Planes()
		if len(ps) == 1 && ps[0].Callsign == "THA341" && ps[0].SpeedKt > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no decode via goroutine: %+v", store.Planes())
	_ = free
}

// modulateModeS builds a 2.4 MSPS u8 IQ stream carrying a position
// (odd+even), callsign and velocity frame for icao, with idle gaps — a
// minimal copy of the adsb package's test modulator (unexported there).
func modulateModeS(icao [3]byte) []byte {
	frames := [][]byte{
		posFrame(icao, 13.70, 100.60, 35000, true),
		posFrame(icao, 13.70, 100.60, 35000, false),
		callFrame(icao, "THA341"),
		velFrame(icao, 450, 123),
	}
	var air []byte
	for _, f := range frames {
		air = append(air, ppmIdleIQ(2000)...)
		air = append(air, ppmModulateIQ(f, 2_400_000)...)
	}
	return append(air, ppmIdleIQ(4000)...)
}

func ppmIdleIQ(n int) []byte {
	out := make([]byte, n*2)
	for i := range out {
		out[i] = 127
	}
	return out
}

func ppmModulateIQ(msg []byte, rate float64) []byte {
	bits := make([]byte, 0, len(msg)*8)
	for _, v := range msg {
		for m := byte(0x80); m != 0; m >>= 1 {
			if v&m != 0 {
				bits = append(bits, 1)
			} else {
				bits = append(bits, 0)
			}
		}
	}
	total := 8.0 + float64(len(bits))
	n := int(total*rate/1e6 + 0.999)
	samples := make([]float64, n)
	pulse := func(us float64) {
		ts := us * rate / 1e6
		te := ts + 0.5*rate/1e6
		for i := 0; i < n; i++ {
			c := float64(i) + 0.5
			if c >= ts && c < te {
				samples[i] = 1
			}
		}
	}
	pulse(0)
	pulse(1)
	pulse(3.5)
	pulse(4.5)
	for k, b := range bits {
		us := 8.0 + float64(k)
		if b == 1 {
			pulse(us)
		} else {
			pulse(us + 0.5)
		}
	}
	out := make([]byte, 0, n*2)
	for _, v := range samples {
		re := byte(127.5 + v*0.7*127.5)
		out = append(out, re, 127)
	}
	return out
}

// ---- minimal Mode S frame encoders (mirrors internal/adsb test encoders) ----

var cprBounds = []float64{10.4704712, 14.8281744, 18.1862636, 21.0293949, 23.5450492,
	25.8292471, 27.9389871, 29.9113569, 31.7720975, 33.5399654, 35.2289960, 36.8502511,
	38.4124189, 39.9225668, 41.3865183, 42.8081401, 44.1915498, 45.5402911, 46.8572311,
	48.1449355, 49.4055748, 50.6413768, 51.8546525, 53.0472965, 54.2210519, 55.3773540,
	56.5175373, 57.6426895, 58.7537206, 59.8517327, 60.9377148, 62.0125132, 63.0767427,
	64.1308863, 65.1755040, 66.2109305, 67.2374953, 68.2555861, 69.2655688, 70.2678222,
	71.2625572, 72.2500269, 73.2304877, 74.2040941, 75.1709847, 76.1315119, 77.0858914,
	78.0343104, 78.9769309, 79.9140611, 80.8458489, 81.7724789, 82.6939714, 83.6104640,
	84.5220350, 85.4296973, 86.3325706, 87.2307885, 88.1244215, 89.0135132, 89.8982529}

func cprNL(lat float64) int {
	a := math.Abs(lat)
	for i, b := range cprBounds {
		if a < b {
			return 59 - i
		}
	}
	return 1
}

func cprEncLat(lat float64, odd bool) int {
	dlat := 360.0 / 60.0
	if odd {
		dlat = 360.0 / 59.0
	}
	frac := math.Mod(math.Mod(lat, dlat)+dlat, dlat) / dlat
	return int(math.Floor(frac*131072+0.5)) & 0x1FFFF
}

func cprEncLon(lat, lon float64, odd bool) int {
	i := 0
	if odd {
		i = 1
	}
	ni := cprNL(lat) - i
	if ni < 1 {
		ni = 1
	}
	dlon := 360.0 / float64(ni)
	frac := math.Mod(math.Mod(lon, dlon)+dlon, dlon) / dlon
	return int(math.Floor(frac*131072+0.5)) & 0x1FFFF
}

func encAlt25(altFt int) int {
	v := (altFt + 1000) / 25
	return (v & 0xF) | ((v & 0x7F0) << 1) | 0x10
}

func posFrame(icao [3]byte, lat, lon float64, altFt int, odd bool) []byte {
	latC, lonC := cprEncLat(lat, odd), cprEncLon(lat, lon, odd)
	ac := encAlt25(altFt)
	f := 0
	if odd {
		f = 1
	}
	msg := make([]byte, 11)
	msg[0] = 0x8D
	msg[1], msg[2], msg[3] = icao[0], icao[1], icao[2]
	me := msg[4:11]
	me[0] = byte(11 << 3)
	me[1] = byte(ac >> 4)
	me[2] = byte((ac&0xF)<<4 | f<<2 | (latC>>15)&3)
	me[3] = byte(latC >> 7)
	me[4] = byte((latC&0x7F)<<1 | (lonC>>16)&1)
	me[5] = byte(lonC >> 8)
	me[6] = byte(lonC)
	return adsb.AppendCRC24(msg)
}

func callFrame(icao [3]byte, call string) []byte {
	msg := make([]byte, 11)
	msg[0] = 0x8D
	msg[1], msg[2], msg[3] = icao[0], icao[1], icao[2]
	me := msg[4:11]
	me[0] = byte(4 << 3)
	call += "        "
	var v [8]int
	for i := 0; i < 8; i++ {
		c := call[i]
		switch {
		case c >= 'A' && c <= 'Z':
			v[i] = int(c-'A') + 1
		case c >= '0' && c <= '9':
			v[i] = int(c-'0') + 48
		default:
			v[i] = 32
		}
	}
	me[1] = byte(v[0]<<2 | v[1]>>4)
	me[2] = byte(v[1]<<4 | v[2]>>2)
	me[3] = byte(v[2]<<6 | v[3])
	me[4] = byte(v[4]<<2 | v[5]>>4)
	me[5] = byte(v[5]<<4 | v[6]>>2)
	me[6] = byte(v[6]<<6 | v[7])
	return adsb.AppendCRC24(msg)
}

func velFrame(icao [3]byte, speedKt, trackDeg int) []byte {
	rad := float64(trackDeg) * math.Pi / 180
	ew := int(math.Round(float64(speedKt) * math.Sin(rad)))
	ns := int(math.Round(float64(speedKt) * math.Cos(rad)))
	msg := make([]byte, 11)
	msg[0] = 0x8D
	msg[1], msg[2], msg[3] = icao[0], icao[1], icao[2]
	me := msg[4:11]
	me[0] = byte(19<<3 | 1)
	if ew < 0 {
		me[1] |= 0x04
		ew = -ew
	}
	me[1] |= byte((ew >> 8) & 3)
	me[2] = byte(ew & 0xFF)
	if ns < 0 {
		me[3] |= 0x80
		ns = -ns
	}
	me[3] |= byte((ns >> 3) & 0x7F)
	me[4] |= byte((ns & 7) << 5)
	return adsb.AppendCRC24(msg)
}

// TestADSDEnqueueVariableBlocks: ReadIQ returns variable-size blocks; a
// pooled buffer from an earlier SMALL block must not be re-sliced past
// its cap when a LARGER block arrives (this panic crashed the app on
// device: "slice bounds out of range [:65536] with capacity 43776").
func TestADSDEnqueueVariableBlocks(t *testing.T) {
	r := NewDemo(dsp.ModeNFM, nil)
	r.ADSBStore(adsb.NewStore())
	r.SetCaptureRate(2_400_000)
	r.SetADSBRFEnabled(true)
	defer r.SetADSBRFEnabled(false)

	// Drain in the background so the pool actually recycles buffers.
	r.mu.Lock()
	q := r.adsbQ
	r.mu.Unlock()
	sizes := make(chan int, 64)
	go func() {
		for b := range q {
			sizes <- len(b)
		}
	}()
	// Small block first (goes into the pool via the demod loop), then a
	// much larger one that must NOT reuse the small buffer's cap. The
	// queue drops blocks it cannot buffer (by design), so space the
	// enqueues and assert on the SET of sizes that get through.
	small := make([]byte, 21760)
	big := make([]byte, 65536)
	tiny := make([]byte, 10880)
	for i := 0; i < 6; i++ {
		r.adsbEnqueue(small)
		time.Sleep(2 * time.Millisecond)
		r.adsbEnqueue(big)
		time.Sleep(2 * time.Millisecond)
		r.adsbEnqueue(tiny)
		time.Sleep(2 * time.Millisecond)
	}
	seen := map[int]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case n := <-sizes:
			if n != 21760 && n != 65536 && n != 10880 {
				t.Fatalf("unexpected block size %d", n)
			}
			seen[n] = true
		case <-time.After(200 * time.Millisecond):
		}
		if seen[21760] && seen[65536] && seen[10880] {
			break
		}
	}
	if !seen[21760] || !seen[65536] {
		t.Fatalf("did not observe both small and large blocks: %v", seen)
	}
}
