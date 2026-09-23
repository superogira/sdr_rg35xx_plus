package dsp

import "math"

// FT8 LDPC(174,91) belief-propagation decoder, CRC-14 and encoder,
// ported from kgoba/ft8_lib (ldpc.c, crc.c, encode.c), which ports
// WSJT-X's ldpc_174_91 code. Sign convention: llr[i] > 0 means bit i is
// more likely to be 1 (log(P1/P0)).

// ft8LDPCCheck returns the number of violated parity checks (0 = valid
// codeword).
func ft8LDPCCheck(bits []int) int {
	errors := 0
	for m := 0; m < 83; m++ {
		x := 0
		for i := 0; i < ft8NumRows[m]; i++ {
			x ^= bits[ft8Nm[m][i]-1]
		}
		if x != 0 {
			errors++
		}
	}
	return errors
}

// ft8BPDecode runs sum-product iterations on the 174 LLRs, filling
// plain with hard decisions. Returns the lowest parity-error count
// seen (0 means a clean codeword).
func ft8BPDecode(llr []float64, maxIters int, plain []int) int {
	var tov [174][3]float64
	var toc [83][7]float64
	minErrors := 83
	for iter := 0; iter < maxIters; iter++ {
		plainSum := 0
		for n := 0; n < 174; n++ {
			plain[n] = 0
			if llr[n]+tov[n][0]+tov[n][1]+tov[n][2] > 0 {
				plain[n] = 1
			}
			plainSum += plain[n]
		}
		if plainSum == 0 {
			break // converged to all-zeros, prohibited
		}
		errors := ft8LDPCCheck(plain)
		if errors < minErrors {
			minErrors = errors
			if errors == 0 {
				break
			}
		}
		// Bit-to-check messages.
		for m := 0; m < 83; m++ {
			for ni := 0; ni < ft8NumRows[m]; ni++ {
				n := ft8Nm[m][ni] - 1
				tnm := llr[n]
				for mi := 0; mi < 3; mi++ {
					if ft8Mn[n][mi] != m {
						tnm += tov[n][mi]
					}
				}
				toc[m][ni] = math.Tanh(-tnm / 2)
			}
		}
		// Check-to-bit messages.
		for n := 0; n < 174; n++ {
			for mi := 0; mi < 3; mi++ {
				m := ft8Mn[n][mi]
				tmn := 1.0
				for ni := 0; ni < ft8NumRows[m]; ni++ {
					if ft8Nm[m][ni]-1 != n {
						tmn *= toc[m][ni]
					}
				}
				tov[n][mi] = -2 * math.Atanh(tmn)
			}
		}
	}
	return minErrors
}

// ft8NormalizeLLR scales the LLR distribution the way ft8_lib does
// (variance → 24), which sets the operating point of the BP decoder.
func ft8NormalizeLLR(llr []float64) {
	var sum, sum2 float64
	for _, v := range llr {
		sum += v
		sum2 += v * v
	}
	invN := 1.0 / float64(len(llr))
	variance := (sum2 - sum*sum*invN) * invN
	if variance <= 1e-12 {
		return
	}
	norm := math.Sqrt(24.0 / variance)
	for i := range llr {
		llr[i] *= norm
	}
}

// ft8CRC14 computes the FT8 CRC-14 (poly 0x2757, MSB-first, init 0)
// over the first numBits bits of the byte-packed message.
func ft8CRC14(msg []byte, numBits int) uint16 {
	const topbit = 1 << 13
	remainder := uint16(0)
	idxByte := 0
	for idxBit := 0; idxBit < numBits; idxBit++ {
		if idxBit%8 == 0 {
			remainder ^= uint16(msg[idxByte]) << 6
			idxByte++
		}
		if remainder&topbit != 0 {
			remainder = (remainder << 1) ^ 0x2757
		} else {
			remainder <<= 1
		}
	}
	return remainder & ((topbit << 1) - 1)
}

// ft8PackBits MSB-packs the first numBits entries of bits into packed.
func ft8PackBits(bits []int, numBits int, packed []byte) {
	for i := range packed {
		packed[i] = 0
	}
	mask := byte(0x80)
	byteIdx := 0
	for i := 0; i < numBits; i++ {
		if bits[i] != 0 {
			packed[byteIdx] |= mask
		}
		mask >>= 1
		if mask == 0 {
			mask = 0x80
			byteIdx++
		}
	}
}

// ft8VerifyCRC checks the decoded codeword's first 91 bits: extracts
// the embedded 14-bit CRC (bits 77..90) and recomputes it over the
// 77-bit payload zero-extended to 82 bits.
func ft8VerifyCRC(plain []int) bool {
	var a91 [12]byte
	ft8PackBits(plain, 91, a91[:])
	extracted := uint16(a91[9]&0x07)<<11 | uint16(a91[10])<<3 | uint16(a91[11])>>5
	a91[9] &= 0xF8
	a91[10] = 0
	calculated := ft8CRC14(a91[:], 96-14)
	return extracted == calculated
}

// ft8CRCExtract returns the embedded 14-bit CRC of the 91-bit codeword
// prefix (diagnostics only).
func ft8CRCExtract(plain []int) uint16 {
	var a91 [12]byte
	ft8PackBits(plain, 91, a91[:])
	return uint16(a91[9]&0x07)<<11 | uint16(a91[10])<<3 | uint16(a91[11])>>5
}

// --- encoder (used by tests to build known waveforms) -----------------

func ft8Parity8(x byte) int {
	x ^= x >> 4
	x ^= x >> 2
	x ^= x >> 1
	return int(x & 1)
}

// ft8Encode174 extends the 91-bit (payload+CRC) message to the 174-bit
// codeword with the LDPC generator matrix.
func ft8Encode174(message []byte, codeword []byte) {
	for j := 0; j < len(codeword); j++ {
		if j < len(message) {
			codeword[j] = message[j]
		} else {
			codeword[j] = 0
		}
	}
	colMask := byte(0x80 >> (91 % 8))
	colIdx := 12 - 1
	for i := 0; i < 83; i++ {
		nsum := 0
		for j := 0; j < 12; j++ {
			bits := message[j] & byte(ft8Generator[i][j])
			nsum ^= ft8Parity8(bits)
		}
		if nsum%2 != 0 {
			codeword[colIdx] |= colMask
		}
		colMask >>= 1
		if colMask == 0 {
			colMask = 0x80
			colIdx++
		}
	}
}

// ft8AddCRC copies the 77-bit payload into a91 and appends its CRC-14.
func ft8AddCRC(payload []byte, a91 []byte) {
	for i := 0; i < 10; i++ {
		a91[i] = payload[i]
	}
	a91[9] &= 0xF8
	a91[10] = 0
	checksum := ft8CRC14(a91, 96-14)
	a91[9] |= byte(checksum >> 11)
	a91[10] = byte(checksum >> 3)
	a91[11] = byte(checksum << 5)
}

// FT8GrayMap maps a 3-bit value to the transmitted tone index.
var FT8GrayMap = [8]int{0, 1, 3, 2, 5, 6, 4, 7}
