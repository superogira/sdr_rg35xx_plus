package dsp

import "fmt"

// FT8Message is a decoded FT8 transmission.
type FT8Message struct {
	CallsignFrom string
	CallsignTo   string
	Grid         string
	Report       string
	Valid        bool
}

const ft8Charset = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func ft8UnpackCall(n uint64) string {
	if n == 0 {
		return ""
	}
	var chars []byte
	for n > 0 {
		chars = append([]byte{ft8Charset[n%37]}, chars...)
		n /= 37
	}
	for len(chars) > 0 && chars[len(chars)-1] == ' ' {
		chars = chars[:len(chars)-1]
	}
	return string(chars)
}

func ft8UnpackGrid(n uint64) string {
	if n == 0 {
		return ""
	}
	return string([]byte{
		byte('A' + n/(18*10*10)%18),
		byte('A' + n/(10*10)%18),
		byte('0' + (n/10)%10),
		byte('0' + n%10),
	})
}

func bitsToInt(bits []int) uint64 {
	var n uint64
	for _, b := range bits {
		n = n<<1 | uint64(b&1)
	}
	return n
}

// DecodeFT8Message unpacks the 77-bit payload.
func DecodeFT8Message(bits []int) FT8Message {
	if len(bits) < 77 {
		return FT8Message{}
	}
	msg := bits[:77]
	msgType := (msg[0] << 2) | (msg[1] << 1) | msg[2]
	switch msgType {
	case 0:
		c1 := bitsToInt(msg[3:31])
		c2 := bitsToInt(msg[31:59])
		g := bitsToInt(msg[59:74])
		cs1, cs2, grid := ft8UnpackCall(c1), ft8UnpackCall(c2), ft8UnpackGrid(g)
		if cs1 == "" && cs2 != "" {
			return FT8Message{CallsignFrom: "CQ", CallsignTo: cs2, Grid: grid, Valid: true}
		}
		return FT8Message{CallsignFrom: cs1, CallsignTo: cs2, Grid: grid, Valid: true}
	case 1:
		c1 := bitsToInt(msg[3:31])
		c2 := bitsToInt(msg[31:59])
		rpt := bitsToInt(msg[59:64])
		cs1, cs2 := ft8UnpackCall(c1), ft8UnpackCall(c2)
		r := ""
		if rpt > 0 && rpt <= 30 {
			r = fmt.Sprintf("-%02d", rpt-1)
		}
		return FT8Message{CallsignFrom: cs1, CallsignTo: cs2, Report: r, Valid: true}
	}
	return FT8Message{Valid: false}
}

// ft8ExtractSymbols: 79 tones → remove sync → bits
func ft8ExtractSymbols(tones []int) []float64 {
	var data []int
	for i := 0; i < 79; i++ {
		if !isSyncPos(i) {
			data = append(data, tones[i])
		}
	}
	var bits []float64
	for _, s := range data {
		bits = append(bits, float64((s>>2)&1), float64((s>>1)&1), float64(s&1))
	}
	for len(bits) < 174 {
		bits = append(bits, 0)
	}
	return bits[:174]
}

func isSyncPos(p int) bool {
	for _, s := range ft8SyncPositions {
		if p == s {
			return true
		}
	}
	return false
}

// DecodeFT8At: audio → tones → bits → message (simplified decode without LDPC).
func DecodeFT8At(audio []float64, centerHz float64) *FT8Message {
	if len(audio) < FT8FrameSamp {
		return nil
	}
	tones := make([]int, 79)
	for sym := 0; sym < 79; sym++ {
		s := sym * FT8SymSamples
		bt, bm := 0, -1.0
		for t := 0; t < 8; t++ {
			m := goertzelMag(audio[s:s+FT8SymSamples], centerHz+(float64(t)-3.5)*FT8ToneHz, float64(FT8AudioRate))
			if m > bm {
				bm, bt = m, t
			}
		}
		tones[sym] = bt
	}
	sc := 0
	for i, e := range ft8SyncCostas {
		if tones[ft8SyncPositions[i]] == e {
			sc++
		}
	}
	if sc < 6 {
		return nil
	}
	bits := make([]int, 174)
	fb := ft8ExtractSymbols(tones)
	for i, f := range fb {
		if f > 0.5 {
			bits[i] = 1
		}
	}
	msg := DecodeFT8Message(bits)
	if !msg.Valid {
		return nil
	}
	return &msg
}
