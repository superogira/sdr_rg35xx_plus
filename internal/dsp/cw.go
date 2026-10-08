package dsp

import (
	"math"
	"strings"
	"sync"
)

// CW (Morse) decoder for the 8 kHz monitor branch.
//
// The app's CW mode listens at dial + 700 Hz (the beat note), so the
// decoder tracks narrowband power around 700 Hz and its envelope.
// Dit length is LEARNED adaptively (beacons/hams span 12-35 wpm) and
// follows drift via EMA. Classification uses ratio margins, not exact
// 1/3/7 units: element <0.6× = dit, >1.4× = dah; gaps likewise, with
// >2.2× a word space.
//
// The envelope threshold adapts between a slow noise floor and peak.

const (
	cwBeatHz   = 700.0
	cwMaxChars = 1200 // rolling history; the big CW window reads it
)

// CWDecoder decodes on-off keyed Morse.
type CWDecoder struct {
	mu sync.Mutex

	// narrowband envelope: complex NCO at 700 Hz + LPF
	ph     float64
	lpI    []float64
	lpQ    []float64
	lpTps  []float64
	lpPos  int
	env    float64
	envMin float64 // rolling envelope minimum (tone-vs-noise proof)

	// adaptive threshold
	floor, peak float64

	// keying state machine
	keyed   bool
	elemN   int // samples in the current keyed element
	gapN    int // samples since the last key-up
	cur     strings.Builder
	spaced  bool      // a word space is pending for this gap
	gapHist []float64 // recent gap lengths (speed bootstrap)
	dotS    float64   // learned dit length in samples
	wpm     float64
	dotSeen bool

	// output
	text      []rune
	lastElemN int // test hook: last committed element length
	drain     []rune
}

// cwProsigns are multi-character sequences sent as one element run
// (no inter-element gap); real CW text uses them constantly.
var cwProsigns = map[string]string{
	".-...":  "<AS>", // wait
	"...-.-": "<SK>", // end of contact
	"-.-.--": "<KN>", // over (specific station)
	"-....-": "-",    // hyphen
}

var cwMorse = map[string]rune{
	".-": 'A', "-...": 'B', "-.-.": 'C', "-..": 'D', ".": 'E',
	"..-.": 'F', "--.": 'G', "....": 'H', "..": 'I', ".---": 'J',
	"-.-": 'K', ".-..": 'L', "--": 'M', "-.": 'N', "---": 'O',
	".--.": 'P', "--.-": 'Q', ".-.": 'R', "...": 'S', "-": 'T',
	"..-": 'U', "...-": 'V', ".--": 'W', "-..-": 'X', "-.--": 'Y',
	"--..":  'Z',
	"-----": '0', ".----": '1', "..---": '2', "...--": '3',
	"....-": '4', ".....": '5', "-....": '6', "--...": '7',
	"---..": '8', "----.": '9',
	".-.-.-": '.', "--..--": ',', "..--..": '?', "-..-.": '/',
	".----.": '\'', "-.-.--": '!', "-...-": '=', ".-.-.": '+',
	"---...": ':', "-.-.-.": ';', "..--.-": '_', ".-..-.": '"',
	".--.-.": '@', "-.--.": '(', "-.--.-": ')', "...-..-": '$',
}

// NewCWDecoder starts at a mid speed (~20 wpm) and adapts.
func NewCWDecoder() *CWDecoder {
	d := &CWDecoder{dotS: 480, floor: 0, peak: 0.2}
	d.lpTps = designCWLp()
	d.lpI = make([]float64, len(d.lpTps))
	d.lpQ = make([]float64, len(d.lpTps))
	d.text = make([]rune, 0, cwMaxChars)
	return d
}

// designCWLp: the envelope lowpass. It must kill the 700 Hz carrier
// ripple (a 10-tap moving average leaves ~30% ripple that shreds weak
// signals) yet track 30 wpm keying (50 Hz). A 64-tap FIR at 100 Hz.
func designCWLp() []float64 {
	return DesignLowpass(63, 100, 8000)
}

// Feed consumes 8 kHz real audio.
func (d *CWDecoder) Feed(x []float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.lpTps)
	for _, s := range x {
		w := 2 * math.Pi * cwBeatHz / 8000
		d.ph += w
		d.lpI[d.lpPos] = s * math.Cos(d.ph)
		d.lpQ[d.lpPos] = -s * math.Sin(d.ph)
		d.lpPos = (d.lpPos + 1) % n
		var I, Q float64
		for k, t := range d.lpTps {
			j := (d.lpPos + k) % n
			I += t * d.lpI[j]
			Q += t * d.lpQ[j]
		}
		mag := math.Sqrt(I*I + Q*Q)
		d.env += 0.6 * (mag - d.env)
		// rolling minimum of the envelope with fast fall, slow rise:
		// pure noise never drops far (its min stays ~40% of peak),
		// while a keyed tone drops to ~0 every gap.
		if d.env < d.envMin {
			d.envMin = d.env
		} else {
			d.envMin += 0.00005 * (d.env - d.envMin)
		}
		// floor tracking: falls fast in quiet gaps (a silent floor
		// proves a real tone exists); NEVER rises while keyed — the
		// floor is the between-keying level by definition, and rising
		// with the tone made the noise gate trip mid-character.
		if d.env < d.floor {
			d.floor += 0.05 * (d.env - d.floor)
		} else if !d.keyed {
			d.floor += 0.0003 * (d.env - d.floor)
		}
		if d.env > d.peak {
			d.peak += 0.1 * (d.env - d.peak)
		} else {
			d.peak += 0.0002 * (d.env - d.peak)
		}
		d.step()
	}
}

func (d *CWDecoder) step() {
	// Noise gate keyed on the FLOOR: between keyings a real tone's
	// envelope collapses to near zero (measured 0.0005 for a weak
	// tone) while pure noise never drops below ~0.01 (measured at
	// 0.028 for 0.3 RMS). A quiet floor proves there is a signal to
	// key; a busy floor means we are listening to noise.
	if d.floor > 0.008 || d.peak < d.floor*3 || d.peak-d.floor < 0.03 {
		// No real signal: treat as key-up (long gap keeps flushing).
		d.keyed = false
		d.gapN++
		if d.gapN == int(2.0*d.dotS)+1 {
			d.flushChar()
		}
		return
	}
	thr := d.floor + 0.45*(d.peak-d.floor)
	now := d.env > thr
	switch {
	case now && !d.keyed:
		// key-down edge. If the last element ended LESS than 0.35 dots
		// ago it was envelope ripple mid-element, not a real key-up:
		// rejoin (keep elemN, keep the symbol gap unclassified).
		if d.elemN > 0 && d.gapN < d.rippleCap() {
			d.keyed = true
			d.gapN = 0
			break
		}
		// Gap ended at this key-down: record it for the speed
		// bootstrap (symbol gaps are 1 dot; the median of the first
		// few is a robust estimate even when elements have fused).
		if !d.dotSeen && d.gapN > 30 && d.gapN < 6000 {
			// A repeated gap (≥3 within 10%) is a symbol gap: real
			// inter-element gaps recur at the same length, while ripple
			// and fused gaps scatter. Lock the dot estimate from it.
			g := float64(d.gapN)
			d.gapHist = append(d.gapHist, g)
			if len(d.gapHist) > 32 {
				d.gapHist = d.gapHist[len(d.gapHist)-32:]
			}
			same := 0
			for _, v := range d.gapHist {
				if v > g*0.9 && v < g*1.1 {
					same++
				}
			}
			if same >= 3 {
				d.dotS = g
				d.dotSeen = true
				d.wpm = 1200.0 / (d.dotS / 8.0)
			}
		}
		// Real gap: classify it. (The gap-growth logic may already have
		// flushed the char / pushed a space — pushing a SECOND space
		// would split every word; only emit what hasn't been emitted.)
		if g := float64(d.gapN) / d.dotS; d.gapN > 0 {
			if g > 2.0 {
				d.flushChar()
				if g > 5.0 && !d.spaced {
					d.push(' ')
					d.spaced = true
				}
			}
		}
		d.gapN = 0
		d.keyed = true
		d.elemN = 0
		d.spaced = false
	case now && d.keyed:
		d.elemN++
		d.gapN = 0
	case !now && d.keyed:
		// Threshold dropped: go not-keyed WITHOUT classifying yet.
		// If the drop is real, the growing-gap logic below flushes
		// the symbol; if it is ripple, the key-down rejoin reabsorbs
		// it within 0.35 dots and elemN kept counting.
		d.keyed = false
		d.gapN = 0
	case !now && !d.keyed:
		d.gapN++
		// A drop shorter than 0.35 dots is ripple mid-element: the
		// element stays pending (elemN untouched — 'now && d.keyed'
		// already counted its samples, gapN must not double-count).
		if d.elemN > 0 && d.gapN < d.rippleCap() {
			break
		}
		// The drop proved real: commit the element by its length.
		if d.elemN > 0 {
			d.commitElement()
		}
		// Long gaps classify as they grow so the last character of a
		// transmission still flushes without a following key-down.
		// Symbol gap 1×, char gap 3×, word gap 7× → split at 2× and 5×.
		if d.gapN == int(2.0*d.dotS)+1 {
			d.flushChar()
		}
		if d.gapN == int(5.0*d.dotS)+1 && !d.spaced {
			d.push(' ')
			d.spaced = true
		}
	}
}

// rippleCap is the longest gap treated as envelope ripple mid-element.
// Before the dot length is learned it is conservative (a quarter of the
// element in progress); once learned it is 0.35 dots.
func (d *CWDecoder) rippleCap() int {
	if d.dotSeen {
		return int(0.35 * d.dotS)
	}
	return int(0.25 * float64(d.elemN+1))
}

// commitElement classifies the finished key-down element into a dit
// or dah symbol and updates the dit-length estimate.
func (d *CWDecoder) commitElement() {
	d.lastElemN = d.elemN // test hook
	r := float64(d.elemN) / d.dotS
	switch {
	case r < 0.35:
		// too short to be real — ignore
	case r < 1.6:
		// Unambiguous dit: trust it for the speed estimate.
		d.cur.WriteByte('.')
		d.learn(d.elemN)
	case r < 2.4:
		// Ambiguous (the estimate is off by ~2× — e.g. a dah-led
		// first character against the 20 wpm default). Classify as
		// dit but DO NOT learn: learning from a mis-scaled element
		// poisoned the estimate and garbled everything after it.
		d.cur.WriteByte('.')
	default:
		d.cur.WriteByte('-')
		d.learn(d.elemN / 3) // unambiguous dah: a third of it is a dit
	}
	d.elemN = 0
}

// learn adapts the dit estimate from a measured dit.
func (d *CWDecoder) learn(n int) {
	if d.dotSeen {
		d.dotS += 0.15 * (float64(n) - d.dotS)
	} else {
		d.dotS = float64(n)
		d.dotSeen = true
	}
	if d.dotS < 120 { // ≈ 80 wpm ceiling
		d.dotS = 120
	}
	d.wpm = 1200.0 / (d.dotS / 8.0) // PARIS: 50 dits/word at 8k sps
}

// flushChar decodes the accumulated element string into a character.
func (d *CWDecoder) flushChar() {
	s := d.cur.String()
	d.cur.Reset()
	if s == "" {
		return
	}
	if p, ok := cwProsigns[s]; ok {
		for _, r := range p {
			d.push(r)
		}
		return
	}
	if r, ok := cwMorse[s]; ok {
		d.push(r)
	} else {
		d.push('*') // unrecognized pattern
	}
}

func (d *CWDecoder) flushWord() { d.flushChar() }

// push appends a rune to the rolling text and the drain queue.
func (d *CWDecoder) push(r rune) {
	d.text = append(d.text, r)
	if len(d.text) > cwMaxChars {
		d.text = d.text[len(d.text)-cwMaxChars:]
	}
	d.drain = append(d.drain, r)
}

// Take drains decoded characters (the web table + device log poll it).
func (d *CWDecoder) Take() []rune {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.drain
	d.drain = nil
	return out
}

// Text returns the rolling last ~160 characters (device mini window).
func (d *CWDecoder) Text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return string(d.text)
}

// WPM returns the currently tracked speed estimate (0 = not measured).
func (d *CWDecoder) WPM() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.wpm
}

// Clear resets the text and speed estimate.
func (d *CWDecoder) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.text = d.text[:0]
	d.drain = d.drain[:0]
	d.dotSeen = false
}
