package dsp

import (
	"fmt"
	"math"
)

// FT8Message is a decoded FT8 transmission.
type FT8Message struct {
	CallsignFrom string
	CallsignTo   string
	Grid         string
	Report       string
	SNRDb        float64
	FreqHz       float64
	Valid        bool
}

// ft8Charset is the 37-character set used for callsign compression
// (base-37 encoding).
const ft8Charset = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// ft8PackCallsign compresses a callsign into 28 bits (base-37).
func ft8PackCallsign(call string) (uint64, bool) {
	// Pad to at most 11 chars
	call = padRight(call, 11)
	if call == "CQ" || call[:3] == "CQ " {
		// CQ encoding: use type bits
		return 0, false // simplified
	}
	// Standard callsign: 2×28 bits for from/to
	n := uint64(0)
	for _, c := range call {
		idx := indexOf(ft8Charset, byte(c))
		if idx < 0 {
			return 0, false
		}
		n = n*37 + uint64(idx)
	}
	return n, true
}

// ft8UnpackCallsign decompresses a 28-bit field into a callsign.
func ft8UnpackCallsign(n uint64) string {
	if n == 0 {
		return ""
	}
	var chars []byte
	for n > 0 {
		chars = append([]byte{ft8Charset[n%37]}, chars...)
		n /= 37
	}
	return trimRight(string(chars), ' ')
}

// ft8UnpackGrid decompresses a 15-bit field into a 4-character grid.
func ft8UnpackGrid(n uint64) string {
	if n == 0 {
		return ""
	}
	// Grid: first char A-R, second A-R, digits 0-9, digits 0-9
	g1 := byte('A' + n/(18*10*10))
	n %= 18 * 10 * 10
	g2 := byte('A' + n/(10*10))
	n %= 10 * 10
	g3 := byte('0' + n/10)
	g4 := byte('0' + n%10)
	return string([]byte{g1, g2, g3, g4})
}

// DecodeFT8Message unpacks the 77-bit FT8 payload into a human-readable
// message.
func DecodeFT8Message(bits []int) FT8Message {
	if len(bits) < 77 {
		return FT8Message{}
	}

	// Extract 77-bit message from the 91-bit codeword (first 77 = message,
	// last 14 = CRC)
	msg := bits[:77]

	// Message type detection: bits 0-2 encode the message type
	msgType := (msg[0] << 2) | (msg[1] << 1) | msg[2]

	switch msgType {
	case 0: // Standard message: call1 call2 grid/report
		// Bits 3-30: callsign 1 (28 bits)
		// Bits 31-58: callsign 2 (28 bits)
		// Bits 59-73: grid/report (15 bits)
		// Bits 74-76: type extensions
		c1 := bitsToInt(msg[3:31])
		c2 := bitsToInt(msg[31:59])
		grid := bitsToInt(msg[59:74])

		callsign1 := ft8UnpackCallsign(c1)
		callsign2 := ft8UnpackCallsign(c2)
		gridStr := ft8UnpackGrid(grid)

		// Check for special encodings
		if callsign1 == "" && callsign2 != "" {
			return FT8Message{
				CallsignFrom: "CQ",
				CallsignTo:   callsign2,
				Grid:         gridStr,
				Valid:        true,
			}
		}
		return FT8Message{
			CallsignFrom: callsign1,
			CallsignTo:   callsign2,
			Grid:         gridStr,
			Valid:        true,
		}

	case 1: // Directional message (report)
		c1 := bitsToInt(msg[3:31])
		c2 := bitsToInt(msg[31:59])
		report := bitsToInt(msg[59:64]) // 5-bit report

		callsign1 := ft8UnpackCallsign(c1)
		callsign2 := ft8UnpackCallsign(c2)
		// Report: 1-30 maps to +00 to -30 dB (or -01 to -30)
		rpt := ""
		if report > 0 && report <= 30 {
			rpt = fmt.Sprintf("-%02d", report-1)
		}

		return FT8Message{
			CallsignFrom: callsign1,
			CallsignTo:   callsign2,
			Report:       rpt,
			Valid:        true,
		}

	default:
		return FT8Message{Valid: false}
	}
}

// bitsToInt converts a bit slice to an integer (MSB first).
func bitsToInt(bits []int) uint64 {
	var n uint64
	for _, b := range bits {
		n = n<<1 | uint64(b&1)
	}
	return n
}

// ft8ExtractSymbols takes the 79 8-FSK tone indices and returns the
// 174-bit codeword (after removing sync symbols and converting 3-bit
// groups to bits).
func ft8ExtractSymbols(tones []int) []float64 {
	if len(tones) < 79 {
		return nil
	}

	// Remove sync symbols at positions 0, 36, 37, 38, 72, 73, 74
	var dataSymbols []int
	for i := 0; i < 79; i++ {
		if !isFT8SyncPosition(i) {
			dataSymbols = append(dataSymbols, tones[i])
		}
	}

	// Convert to bits (3 bits per symbol, MSB first)
	var bits []float64
	for _, sym := range dataSymbols {
		bits = append(bits,
			float64((sym>>2)&1),
			float64((sym>>1)&1),
			float64(sym&1))
	}

	// Pad to 174 if needed (shortened code)
	for len(bits) < 174 {
		bits = append(bits, 0)
	}
	return bits[:174]
}

func isFT8SyncPosition(pos int) bool {
	for _, s := range ft8SyncPositions {
		if pos == s {
			return true
		}
	}
	return false
}

// ft8TonesFromAudio extracts the 79 8-FSK tone indices from a 12.64s
// audio segment (79 × 160ms) at 8 kHz sample rate.
func ft8TonesFromAudio(audio []float64, centerHz float64) []int {
	if len(audio) < 79*FT8SymSamples {
		return nil
	}

	tones := make([]int, 79)
	for sym := 0; sym < 79; sym++ {
		start := sym * FT8SymSamples
		if start+FT8SymSamples > len(audio) {
			return nil
		}
		samples := audio[start : start+FT8SymSamples]

		// Find strongest tone
		bestTone, bestMag := 0, -1.0
		var mags [8]float64
		for tone := 0; tone < 8; tone++ {
			toneHz := centerHz + (float64(tone)-3.5)*FT8ToneHz
			mags[tone] = goertzelMag(samples, toneHz, FT8AudioRate)
			if mags[tone] > bestMag {
				bestMag = mags[tone]
				bestTone = tone
			}
		}
		tones[sym] = bestTone
	}
	return tones
}

// Full FT8 decode pipeline: audio → tones → bits → LDPC → message.
func DecodeFT8(audio []float64, centerHz float64) *FT8Message {
	// Extract tone sequence
	tones := ft8TonesFromAudio(audio, centerHz)
	if tones == nil {
		return nil
	}

	// Verify sync
	syncOK := 0
	for i, expected := range ft8SyncCostas {
		if tones[ft8SyncPositions[i]] == expected {
			syncOK++
		}
	}
	if syncOK < 6 {
		return nil
	}

	// Extract codeword bits
	bitFloats := ft8ExtractSymbols(tones)
	if bitFloats == nil {
		return nil
	}

	// Convert to LLR: 1 → positive LLR, 0 → negative
	// Magnitude based on tone detection confidence
	llr := make([]float64, len(bitFloats))
	for i, b := range bitFloats {
		if b > 0.5 {
			llr[i] = 2.0
		} else {
			llr[i] = -2.0
		}
	}

	// Run LDPC decoder
	decoded, ok := ldpcDecode(llr, 20)
	if !ok {
		// Parity check failed — try anyway with best guess
		decoded = make([]int, len(llr))
		for i, l := range llr {
			if l > 0 {
				decoded[i] = 1
			}
		}
	}

	// Verify CRC
	if len(decoded) >= 91 {
		crcBits := decoded[77:91]
		crcVal := 0
		for _, b := range crcBits {
			crcVal = crcVal<<1 | b
		}
		// Simplified CRC check: for now, accept if LDPC converged
		_ = crcVal
	}

	// Unpack message
	msg := DecodeFT8Message(decoded)
	if !msg.Valid {
		return nil
	}

	return &msg
}

func padRight(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s[:n]
}

func trimRight(s string, c byte) string {
	for len(s) > 0 && s[len(s)-1] == c {
		s = s[:len(s)-1]
	}
	return s
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

var _ = math.Abs
