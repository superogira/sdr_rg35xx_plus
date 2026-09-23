package dsp

import (
	"fmt"
	"strings"
)

// FT8Message is a decoded FT8 transmission. Text is the full decoded
// message exactly as WSJT-X would print it (e.g. "CQ HS0ZKO OK04" or
// "E23BC W1AW FN42 -07"). Valid is only set when both the LDPC parity
// checks and the CRC-14 pass, so Text can be trusted as received.
// SNRDb is the estimated signal strength above the tone-row noise.
type FT8Message struct {
	Text  string
	Valid bool
	SNRDb  float64
	FreqHz float64
}

const (
	ft8NTokens  = 2063592
	ft8Max22    = 4194304
	ft8MaxGrid4 = 32400
)

const (
	ft8A1 = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ" // 37
	ft8A2 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"  // 36
	ft8A3 = "0123456789"                            // 10
	ft8A4 = " ABCDEFGHIJKLMNOPQRSTUVWXYZ"           // 27
	ft8C  = " 0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ+-./?"
)

// ft8Unpack28 expands a 28-bit callsign field (packjt77 unpack28).
func ft8Unpack28(n28 uint64) string {
	if n28 < ft8NTokens {
		switch {
		case n28 == 0:
			return "DE"
		case n28 == 1:
			return "QRZ"
		case n28 == 2:
			return "CQ"
		case n28 <= 1002:
			return fmt.Sprintf("CQ_%03d", n28-3)
		case n28 <= 532443:
			v := n28 - 1003
			b := [4]byte{
				ft8A4[v/(27*27*27)],
				ft8A4[(v/(27*27))%27],
				ft8A4[(v/27)%27],
				ft8A4[v%27],
			}
			return "CQ " + strings.TrimSpace(string(b[:]))
		}
		return "<tok>"
	}
	n := int64(n28) - ft8NTokens
	if n < ft8Max22 {
		return "<...>" // 22-bit hash of a callsign heard earlier
	}
	n -= ft8Max22
	i1 := n / (36 * 10 * 27 * 27 * 27)
	n -= i1 * (36 * 10 * 27 * 27 * 27)
	i2 := n / (10 * 27 * 27 * 27)
	n -= i2 * (10 * 27 * 27 * 27)
	i3 := n / (27 * 27 * 27)
	n -= i3 * (27 * 27 * 27)
	i4 := n / (27 * 27)
	n -= i4 * (27 * 27)
	i5 := n / 27
	i6 := n % 27
	call := []byte{
		ft8A1[i1], ft8A2[i2], ft8A3[i3],
		ft8A4[i4], ft8A4[i5], ft8A4[i6],
	}
	return strings.TrimSpace(string(call))
}

// ft8UnpackGrid15 expands the 15-bit field: a 4-character grid square,
// or one of the fixed reports (RRR / RR73 / 73 / ±dB).
func ft8UnpackGrid15(v uint64, rBit int) (string, bool) {
	if v < ft8MaxGrid4 {
		g := string([]byte{
			byte('A' + v/1800),
			byte('A' + (v/100)%18),
			byte('0' + (v/10)%10),
			byte('0' + v%10),
		})
		if rBit == 1 {
			return "R " + g, true
		}
		return g, true
	}
	irpt := int(v) - ft8MaxGrid4
	var s string
	switch irpt {
	case 1:
		s = ""
	case 2:
		s = "RRR"
	case 3:
		s = "RR73"
	case 4:
		s = "73"
	default:
		if irpt < 0 || irpt > 70 {
			return "", false
		}
		s = fmt.Sprintf("%+03d", irpt-35)
	}
	if rBit == 1 {
		return "R " + s, true
	}
	return s, true
}

// ft8UnpackText77 decodes free text (13 chars, base-42, 71 bits).
func ft8UnpackText77(bits []int) string {
	n := uint64(0)
	for i := 0; i < 71; i++ {
		n = n<<1 | uint64(bits[i]&1)
	}
	chars := make([]byte, 13)
	for i := 12; i >= 0; i-- {
		chars[i] = ft8C[n%42]
		n /= 42
	}
	return strings.TrimSpace(string(chars))
}

// ft8UnpackC58 decodes the base-38 callsign of type-4 (nonstandard
// call) messages.
func ft8UnpackC58(n58 uint64) string {
	const c38 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ+/ "
	var b [11]byte
	for i := 10; i >= 0; i-- {
		b[i] = c38[n58%38]
		n58 /= 38
	}
	return strings.TrimSpace(string(b[:]))
}

// ft8Unpack77 turns the 77 payload bits into the message text.
func ft8Unpack77(b []int) string {
	get := func(from, n int) uint64 {
		v := uint64(0)
		for i := 0; i < n; i++ {
			v = v<<1 | uint64(b[from+i]&1)
		}
		return v
	}
	i3 := int(get(74, 3))
	n3 := int(get(71, 3))

	switch {
	case i3 == 0 && n3 == 0: // free text
		return ft8UnpackText77(b)

	case i3 == 0 && n3 == 1: // DXpedition: K1ABC RR73; W9XYZ <...> -11
		call1 := ft8Unpack28(get(0, 28))
		call2 := ft8Unpack28(get(28, 28))
		rpt := fmt.Sprintf("%+03d", int(get(66, 5))*2-30)
		return fmt.Sprintf("%s RR73; %s <...> %s", call1, call2, rpt)

	case i3 == 0 && n3 == 2: // EU VHF: PA3XYZ/P R 590003 IO91NP
		call1 := ft8Unpack28(get(0, 28))
		if get(28, 1) == 1 {
			call1 += "/P"
		}
		nrs := 52 + int(get(30, 3))
		serial := int(get(33, 12))
		g6 := get(45, 25)
		grid6 := string([]byte{
			byte('A' + g6/(18*10*10*24*24)%18),
			byte('A' + g6/(10*10*24*24)%18),
			byte('0' + g6/(10*24*24)%10),
			byte('0' + g6/(24*24)%10),
			byte('A' + (g6/24)%24),
			byte('A' + g6%24),
		})
		r := ""
		if get(29, 1) == 1 {
			r = "R "
		}
		return fmt.Sprintf("%s %s%02d %04d %s", call1, r, nrs, serial, grid6)

	case i3 == 0 && n3 == 5: // telemetry, 18 hex digits
		hi := get(0, 23)
		mid := get(23, 24)
		lo := get(47, 24)
		return fmt.Sprintf("%06X%06X%06X", hi, mid, lo)

	case i3 == 1 || i3 == 2: // standard message (the common case)
		call1 := ft8Unpack28(get(0, 28))
		call2 := ft8Unpack28(get(29, 28))
		sfx := ""
		if i3 == 1 {
			sfx = "/R"
		} else {
			sfx = "/P"
		}
		if get(28, 1) == 1 && len(call1) >= 4 {
			call1 += sfx
		}
		if get(57, 1) == 1 && len(call2) >= 4 {
			call2 += sfx
		}
		tail, ok := ft8UnpackGrid15(get(59, 15), int(get(58, 1)))
		if !ok {
			return call1 + " " + call2
		}
		if tail == "" {
			return call1 + " " + call2
		}
		return call1 + " " + call2 + " " + tail

	case i3 == 3: // ARRL RTTY Round-Up: calls + 599 + serial/mult
		call1 := ft8Unpack28(get(1, 28))
		call2 := ft8Unpack28(get(29, 28))
		rpt := fmt.Sprintf("5%dm9", int(get(57, 3))+2)
		exch := int(get(61, 13))
		prefix := ""
		if get(0, 1) == 1 {
			prefix = "TU; "
		}
		r := ""
		if get(58, 1) == 1 {
			r = "R "
		}
		if exch >= 1 && exch <= 7999 {
			return fmt.Sprintf("%s%s %s %s%s%04d", prefix, call1, call2, r, rpt, exch)
		}
		return fmt.Sprintf("%s%s %s %s%s<%d>", prefix, call1, call2, r, rpt, exch)

	case i3 == 4: // nonstandard callsigns
		c11 := ft8UnpackC58(get(12, 58))
		nrpt := int(get(71, 2))
		if get(73, 1) == 1 {
			return "CQ " + c11
		}
		switch nrpt {
		case 0:
			s := ""
			if get(70, 1) == 0 {
				return "<...> " + c11 + s
			}
			return c11 + " <...>" + s
		case 1, 2, 3:
			suffix := []string{"RRR", "RR73", "73"}[nrpt-1]
			if get(70, 1) == 0 {
				return "<...> " + c11 + " " + suffix
			}
			return c11 + " <...> " + suffix
		}
		return c11

	case i3 == 5: // WWROF contest
		call1 := ft8Unpack28(get(1, 28))
		call2 := ft8Unpack28(get(29, 28))
		rpt := fmt.Sprintf("%+03d", int(get(59, 7))-35)
		ex := int(get(66, 9))
		field := string([]byte{byte('A' + ex/18), byte('A' + ex%18)})
		prefix := ""
		if get(0, 1) == 1 {
			prefix = "TU; "
		}
		r := ""
		if get(58, 1) == 1 {
			r = "R"
		}
		return fmt.Sprintf("%s%s %s %s%s %s", prefix, call1, call2, r, rpt, field)
	}
	return fmt.Sprintf("<i3=%d n3=%d>", i3, n3)
}

// ft8DecodeCodeword runs LDPC + CRC over the 174 soft LLRs and returns
// the decoded message when both checks pass. The second return explains
// a failure ("ldpc=N" parity errors remain, "crc" checksum mismatch)
// for the diagnostic log.
func ft8DecodeCodeword(llr []float64) (*FT8Message, string) {
	ft8NormalizeLLR(llr)
	plain := make([]int, 174)
	if errs := ft8BPDecode(llr, 20, plain); errs != 0 {
		return nil, fmt.Sprintf("ldpc=%d", errs)
	}
	if !ft8VerifyCRC(plain) {
		var a91 [12]byte
		ft8PackBits(plain, 91, a91[:])
		extracted := ft8CRCExtract(plain)
		a91[9] &= 0xF8
		a91[10] = 0
		calculated := ft8CRC14(a91[:], 96-14)
		return nil, fmt.Sprintf("crc:%04x!=%04x", extracted, calculated)
	}
	return &FT8Message{Text: ft8Unpack77(plain[:77]), Valid: true}, ""
}
