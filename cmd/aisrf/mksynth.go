package main

import (
	"fmt"
	"math"
	"os"
)

// mkSynth writes a 1.024 Msps CU8 file with one AIS burst on each
// channel at ±25 kHz, mirroring the live recording format.
func mkSynth(path string) {
	armor := "15M67FC000G?ufbE`FepT@3n00Sa"
	var bits6 []byte
	for k := 0; k < len(armor); k++ {
		v := armor[k] - 48
		if v > 40 {
			v -= 8
		}
		for b := 5; b >= 0; b-- {
			bits6 = append(bits6, (v>>uint(b))&1)
		}
	}
	payload := make([]byte, 21)
	for i := 0; i+7 < len(bits6); i += 8 {
		by := byte(0)
		for k := 0; k < 8; k++ {
			by |= bits6[i+k] << (7 - k)
		}
		payload[i/8] = by
	}
	// frame bits: training + flags + stuffed payload+FCS
	var bits []byte
	for i := 0; i < 24; i++ {
		bits = append(bits, byte(i%2))
	}
	flag := func() { bits = append(bits, 0, 1, 1, 1, 1, 1, 1, 0) }
	flag()
	// FCS per the air-order reflected loop: state after the payload's
	// MSB-first bits, transmitted complemented with register-order
	// bits (verified to close the 0xF0B8 residue).
	var air []byte
	for _, by := range payload {
		for k := 0; k < 8; k++ {
			air = append(air, (by>>uint(k))&1)
		}
	}
	c := uint32(0xFFFF)
	for _, b := range air {
		if (uint32(b)^c)&1 != 0 {
			c = (c >> 1) ^ 0x8408
		} else {
			c >>= 1
		}
	}
	inv := uint16(^c)
	for i := 0; i < 16; i++ {
		air = append(air, byte((inv>>uint(i))&1))
	}
	ones := 0
	for _, b := range air {
		bits = append(bits, b)
		if b == 1 {
			ones++
		} else {
			ones = 0
		}
		if ones == 5 {
			bits = append(bits, 0)
			ones = 0
		}
	}
	flag()

	f, err := os.Create(path)
	if err != nil {
		fmt.Println("create:", err)
		os.Exit(1)
	}
	defer f.Close()
	rate := 1024000.0
	spb := rate / 9600
	lvl := 1.0
	cur := 0.0
	ph := [2]float64{}
	seed := 12345.0
	rnd := func() float64 { seed = math.Mod(seed*1103515245+12345, 2147483648); return seed/2147483648 - 0.5 }
	w := func(re, im float64) {
		f.Write([]byte{byte(127.5 + re), byte(127.5 + im)})
	}
	for i := 0; i < int(rate); i++ {
		w(rnd()*8, rnd()*8)
	}
	for bi, b := range bits {
		if b == 0 {
			lvl = -lvl
		}
		start := int(float64(bi) * spb)
		end := int(float64(bi+1) * spb)
		for s := start; s < end; s++ {
			cur += 0.45 * (lvl - cur) / 16.0
			ph[0] += 2 * math.Pi * (2400*cur - 25000) / rate
			ph[1] += 2 * math.Pi * (2400*cur + 25000) / rate
			w(40*math.Cos(ph[0])+40*math.Cos(ph[1])+rnd()*8,
				40*math.Sin(ph[0])+40*math.Sin(ph[1])+rnd()*8)
		}
	}
	for i := 0; i < int(rate); i++ {
		w(rnd()*8, rnd()*8)
	}
	fmt.Println("wrote", path)
}

func x25crc(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, ch := range b {
		crc ^= uint16(ch)
		for k := 0; k < 8; k++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ 0x8408
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFF
}
