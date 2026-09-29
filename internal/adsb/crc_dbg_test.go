package adsb

import "testing"

func refCRC(data []byte) uint32 {
	c := uint32(0)
	for _, d := range data {
		for i := 0; i < 8; i++ {
			fb := (c ^ uint32(d<<i)) & 0x800000
			c <<= 1
			if fb != 0 {
				c ^= 0xFFF409
			}
		}
	}
	return c & 0xFFFFFF
}

func TestCRCDbg(t *testing.T) {
	data := []byte{0x8D, 0x40, 0x6B, 0x90, 0x20, 0x15, 0xA6, 0x78, 0xD4, 0xD2, 0x20}
	t.Logf("ref  = %06X", refCRC(data))
	t.Logf("ours = %06X", crc24(bytesToBits(data)))
}
