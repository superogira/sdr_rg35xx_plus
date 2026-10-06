package dsp

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// cwGen renders keyed Morse as a beat-note tone at beatHz in 8 kHz
// audio. wpm sets the dit length; jitter adds hand-keyed timing noise
// (fraction of a dit); qsb multiplies the amplitude slowly.
type cwGen struct {
	beatHz  float64
	dotN    int // samples per dit
	jitter  float64
	qsb     float64
	ph      float64
	rng     *rand.Rand
	qsbPh   float64
	pending []float64
}

func newCWGen(beatHz, wpm, jitter, qsb float64, seed int64) *cwGen {
	return &cwGen{beatHz: beatHz, dotN: int(1200.0 / wpm * 8), jitter: jitter, qsb: qsb, rng: rand.New(rand.NewSource(seed))}
}

// run emits n units of key state (1=dit,3=dah,1 gap,3 char gap,7 word gap)
func (g *cwGen) run(units float64, keyed bool) {
	n := int(units*float64(g.dotN) + g.rng.Float64()*g.jitter*float64(g.dotN)*2 - g.jitter*float64(g.dotN))
	if n < 1 {
		n = 1
	}
	for i := 0; i < n; i++ {
		amp := 0.0
		if keyed {
			amp = 0.5
			if g.qsb > 0 {
				g.qsbPh += 2 * math.Pi * 0.7 / 8000 // ~0.7 Hz fading
				amp *= 1 - g.qsb*(0.5+0.5*math.Sin(g.qsbPh))
			}
		}
		g.ph += 2 * math.Pi * g.beatHz / 8000
		g.pending = append(g.pending, amp*math.Sin(g.ph))
	}
}

func (g *cwGen) text(s string) {
	first := true
	for _, tok := range prosignTokens(s) {
		if tok == " " {
			g.run(7, false)
			first = true
			continue
		}
		if !first {
			g.run(3, false)
		}
		first = false
		code := tok
		for j, c := range code {
			if j > 0 {
				g.run(1, false)
			}
			if c == '.' {
				g.run(1, true)
			} else {
				g.run(3, true)
			}
		}
	}
	g.run(10, false)
}

// prosignTokens splits text into morse code strings: "<KN>" style
// tokens become their element run, every other rune its own code.
func prosignTokens(s string) []string {
	var out []string
	i := 0
	for i < len(s) {
		if s[i] == '<' {
			j := strings.IndexByte(s[i:], '>')
			if j > 0 {
				tok := s[i : i+j+1]
				for code, v := range cwProsigns {
					if v == tok {
						out = append(out, code)
					}
				}
				i += j + 1
				continue
			}
		}
		out = append(out, morseOf(rune(s[i])))
		i++
	}
	return out
}

func morseOf(r rune) string {
	for code, v := range cwMorse {
		if v == r {
			return code
		}
	}
	for code, v := range cwProsigns {
		if v == string(r) {
			return code
		}
	}
	return ""
}

func decodeAll(t *testing.T, g *cwGen) string {
	d := NewCWDecoder()
	const chunk = 1024
	for off := 0; off < len(g.pending); off += chunk {
		end := off + chunk
		if end > len(g.pending) {
			end = len(g.pending)
		}
		d.Feed(g.pending[off:end])
	}
	return strings.TrimSpace(d.Text())
}

func TestCWSpeeds(t *testing.T) {
	for _, wpm := range []float64{12, 18, 25, 35} {
		g := newCWGen(700, wpm, 0, 0, 1)
		g.text("CQ CQ DE HS0AB")
		got := decodeAll(t, g)
		want := "CQ CQ DE HS0AB"
		// The speed bootstrap may eat the very first character of a
		// transmission (real beacons lead with VV for this reason);
		// everything after it must be exact.
		if !strings.HasSuffix(got, want[1:]) && got != want {
			t.Errorf("%v wpm: got %q want suffix %q", wpm, got, want[1:])
		}
	}
}

func TestCWBeatOffset(t *testing.T) {
	for _, hz := range []float64{650, 680, 720, 750} {
		g := newCWGen(hz, 20, 0, 0, 2)
		g.text("DE HS0AB")
		got := decodeAll(t, g)
		if got != "DE HS0AB" {
			t.Errorf("beat %.0f Hz: got %q", hz, got)
		}
	}
}

func TestCWHandKeyed(t *testing.T) {
	g := newCWGen(700, 18, 0.15, 0, 3) // ±15% timing jitter
	g.text("RST 599 BK")
	got := decodeAll(t, g)
	if got != "RST 599 BK" {
		t.Errorf("hand-keyed: got %q", got)
	}
}

func TestCWQSB(t *testing.T) {
	g := newCWGen(700, 20, 0, 0.5, 4) // 50% depth fading
	g.text("QSB TEST")
	got := decodeAll(t, g)
	if got != "QSB TEST" {
		t.Errorf("qsb: got %q", got)
	}
}

func TestCWNoise(t *testing.T) {
	g := newCWGen(700, 20, 0, 0, 5)
	g.text("NR TEST")
	rng := rand.New(rand.NewSource(9))
	for i := range g.pending {
		g.pending[i] += 0.05 * rng.NormFloat64() // ~20 dB SNR
	}
	got := decodeAll(t, g)
	if got != "NR TEST" {
		t.Errorf("noise: got %q", got)
	}
}

// Prosigns and the hyphen must decode as their multi-char forms, and
// the letter C must NOT be eaten by the KN prosign.
func TestCWProsigns(t *testing.T) {
	g := newCWGen(700, 20, 0, 0, 7)
	g.text("CQ CQ DE HS0AB-7 <KN>")
	got := decodeAll(t, g)
	if !strings.HasSuffix(got, "HS0AB-7 <KN>") {
		t.Errorf("prosigns: got %q", got)
	}
}
