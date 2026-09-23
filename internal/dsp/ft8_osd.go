package dsp

import "sort"

// Ordered-statistics decoding for the FT8 LDPC(174,91) code — the
// fallback WSJT-X runs when belief propagation fails. Fossorier & Lin
// order-ℓ: order the 174 bit positions by |LLR|, Gauss-Jordan the
// generator onto the 91 most-reliable positions (the MRB), hard-decide
// those, re-encode a candidate codeword, then sweep flips of the ℓ
// least-reliable MRB positions keeping the candidate closest (Hamming)
// to the hard decisions. Returns a codeword's 174 bits or nil.

const ft8OSDOrder = 8

// row174 is a packed 174-bit row (192 bits of storage).
type row174 [3]uint64

func (r *row174) bit(p int) int {
	return int(r[p/64] >> uint(63-p%64) & 1)
}

func (r *row174) setBit(p int) {
	r[p/64] |= 1 << uint(63-p%64)
}

func row174Xor(a, b row174) row174 {
	return row174{a[0] ^ b[0], a[1] ^ b[1], a[2] ^ b[2]}
}

func row174Weight(a, b row174) int {
	x := row174Xor(a, b)
	return popcount64(x[0]) + popcount64(x[1]) + popcount64(x[2])
}

func popcount64(v uint64) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

// genMessageBit returns message bit j of parity row i in the packed
// generator (bytes are MSB-first over the 91 message bits).
func genMessageBit(i, j int) int {
	return int(ft8Generator[i][j/8] >> uint(7-j%8) & 1)
}

// ft8OSD attempts an order-ℓ OSD decode. llr follows the BP sign
// convention (positive → bit 1). Returns nil if the MRB is singular.
func ft8OSD(llr []float64, order int) []int {
	const n, k = 174, 91

	// Reliability order, most reliable first.
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return absF(llr[idx[a]]) > absF(llr[idx[b]])
	})

	// Full generator G (91×174) with columns permuted by idx: row j has
	// 1 at permuted position p iff the original column idx[p] is the
	// systematic bit j, or parity bit i = idx[p]-91 whose row carries
	// message bit j.
	var rows [91]row174
	for j := 0; j < k; j++ {
		for p := 0; p < n; p++ {
			c := idx[p]
			if c == j || (c >= k && genMessageBit(c-k, j) != 0) {
				rows[j].setBit(p)
			}
		}
	}

	// Gauss-Jordan the first k columns to the identity. The top-91
	// reliable positions are occasionally dependent for this code
	// (observed at column 90); the standard remedy swaps in the next
	// usable non-basis column, which keeps the code valid — only the
	// basis's reliability ordering degrades slightly.
	swapCol := func(a, b int) {
		for rr := 0; rr < k; rr++ {
			ba := rows[rr].bit(a)
			bb := rows[rr].bit(b)
			if ba != bb {
				rows[rr].flip(a)
				rows[rr].flip(b)
			}
		}
		idx[a], idx[b] = idx[b], idx[a]
	}
	for col := 0; col < k; col++ {
		pivot := -1
		for rr := col; rr < k; rr++ {
			if rows[rr].bit(col) == 1 {
				pivot = rr
				break
			}
		}
		if pivot < 0 {
			// look for a replaceable non-basis column
			found := false
			for p := k; p < n && !found; p++ {
				for rr := col; rr < k; rr++ {
					if rows[rr].bit(p) == 1 {
						swapCol(col, p)
						found = true
						break
					}
				}
			}
			if !found {
				return nil
			}
			for rr := col; rr < k; rr++ {
				if rows[rr].bit(col) == 1 {
					pivot = rr
					break
				}
			}
			if pivot < 0 {
				return nil
			}
		}
		rows[col], rows[pivot] = rows[pivot], rows[col]
		for rr := 0; rr < k; rr++ {
			if rr != col && rows[rr].bit(col) == 1 {
				rows[rr] = row174Xor(rows[rr], rows[col])
			}
		}
	}

	// Permuted hard decisions.
	var hb row174
	for p := 0; p < n; p++ {
		if llr[idx[p]] > 0 {
			hb.setBit(p)
		}
	}

	// Base candidate: XOR of the rows selected by the MRB hard bits —
	// the reduced generator makes this [y_M | parity].
	encodeSel := func(sel row174) row174 {
		var cw row174
		for j := 0; j < k; j++ {
			if sel.bit(j) == 1 {
				cw = row174Xor(cw, rows[j])
			}
		}
		return cw
	}
	var mrb row174
	for j := 0; j < k; j++ {
		if hb.bit(j) == 1 {
			mrb.setBit(j)
		}
	}
	best := encodeSel(mrb)
	bestW := row174Weight(best, hb)

	// Flip subsets of the ℓ least-reliable MRB positions (the tail of
	// the reliability-sorted first k).
	if order > k {
		order = k
	}
	weakest := make([]int, order)
	for i := 0; i < order; i++ {
		weakest[i] = k - 1 - i
	}
	for mask := 1; mask < 1<<uint(order); mask++ {
		sel := mrb
		for i := 0; i < order; i++ {
			if mask>>uint(i)&1 == 1 {
				p := weakest[i]
				if sel.bit(p) == 1 {
					sel[p/64] &^= 1 << uint(63-p%64)
				} else {
					sel.setBit(p)
				}
			}
		}
		cw := encodeSel(sel)
		if w := row174Weight(cw, hb); w < bestW {
			best, bestW = cw, w
		}
	}

	// Un-permute into original bit order.
	bits := make([]int, n)
	for p := 0; p < n; p++ {
		bits[idx[p]] = best.bit(p)
	}
	if ft8LDPCCheck(bits) != 0 {
		return nil // construction guarantees a codeword; safety net
	}
	return bits
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func (r *row174) flip(p int) {
	r[p/64] ^= 1 << uint(63-p%64)
}
