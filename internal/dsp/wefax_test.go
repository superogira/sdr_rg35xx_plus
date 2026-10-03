package dsp

import (
	"image/png"
	"math"
	"math/rand"
	"os"
	"testing"
)

// wefaxSynth builds 8 kHz audio for a fax transmission: an FM
// subcarrier at 1900 Hz ±400 (black 1500, white 2300).
type wefaxSynth struct {
	ph  float64
	out []float64
}

func (s *wefaxSynth) tone(v float64, n int) { // v: 0 black .. 1 white
	for i := 0; i < n; i++ {
		f := 1500 + 800*v
		s.ph += 2 * math.Pi * f / WefaxRate
		s.out = append(s.out, 0.5*math.Sin(s.ph))
	}
}

// apt: square-wave black/white toggling at hz for sec seconds.
func (s *wefaxSynth) apt(hz float64, sec float64) {
	n := int(sec * WefaxRate)
	half := WefaxRate / (2 * hz)
	for i := 0; i < n; i++ {
		v := 0.0
		if int(float64(i)/half)%2 == 1 {
			v = 1
		}
		s.tone(v, 1)
	}
}

// pattern: vertical bars — black for the left half of each 1/8 of the
// line, white for the right half. Easy to verify column-wise.
func barAt(x float64) float64 {
	if int(x*8)%2 == 0 {
		return 0
	}
	return 1
}

func TestWefaxDecodesSyntheticChart(t *testing.T) {
	const spl = WefaxRate * 60 / WefaxLPM // 4000 samples/line
	s := &wefaxSynth{}
	s.tone(1, WefaxRate/2) // a bit of white carrier
	s.apt(300, 4)          // start tone (IOC 576)
	// phasing: 30 lines of black with a 5% white pulse at line start
	for l := 0; l < 30; l++ {
		s.tone(1, spl*5/100)
		s.tone(0, spl-spl*5/100)
	}
	// image: 200 lines of 8 vertical bars
	const imgLines = 200
	for l := 0; l < imgLines; l++ {
		for i := 0; i < spl; i++ {
			s.tone(barAt(float64(i)/spl), 1)
		}
	}
	s.apt(450, 4) // stop tone
	// add noise so the test is not trivially clean
	rng := rand.New(rand.NewSource(1))
	for i := range s.out {
		s.out[i] += rng.NormFloat64() * 0.05
	}

	d := NewWefaxDecoder()
	d.SetAutoSave(true)
	for i := 0; i < len(s.out); i += 512 {
		j := i + 512
		if j > len(s.out) {
			j = len(s.out)
		}
		d.Feed(s.out[i:j])
	}
	done := d.TakeDone()
	if len(done) != 1 {
		t.Fatalf("completed images = %d, want 1 (start→stop)", len(done))
	}
	img := done[0]
	lines := img.Bounds().Dy()
	if os.Getenv("SDR_WEFAXSHOT") != "" {
		f, _ := os.Create("wefax_synth.png")
		png.Encode(f, img)
		f.Close()
	}
	if lines < imgLines-5 || lines > imgLines+8 {
		t.Fatalf("image lines = %d, want ≈%d", lines, imgLines)
	}
	// column-wise check on the middle 80% of rows: each bar region must
	// read mostly black/white as sent (allow edge smear near transitions)
	good, total := 0, 0
	for y := lines / 10; y < lines*9/10; y++ {
		for x := 0; x < WefaxWidth; x++ {
			fx := float64(x) / WefaxWidth
			// skip pixels within 2% of a bar edge
			if d := math.Mod(fx*8, 1); d < 0.08 || d > 0.92 {
				continue
			}
			want := barAt(fx)
			got := float64(img.GrayAt(x, y).Y) / 255
			total++
			if math.Abs(got-want) < 0.35 {
				good++
			}
		}
	}
	acc := float64(good) / float64(total)
	if acc < 0.95 {
		t.Fatalf("pixel accuracy %.3f, want ≥0.95 (alignment/demod wrong)", acc)
	}
	t.Logf("lines=%d accuracy=%.4f", lines, acc)
}

func TestWefaxFreeRunWithoutAPT(t *testing.T) {
	// Tuned in mid-chart: no start tone, must still draw rows.
	const spl = WefaxRate * 60 / WefaxLPM
	s := &wefaxSynth{}
	for l := 0; l < 40; l++ {
		for i := 0; i < spl; i++ {
			s.tone(barAt(float64(i)/spl), 1)
		}
	}
	d := NewWefaxDecoder()
	d.Feed(s.out)
	prev, lines, _ := d.Preview(200, 100)
	if lines < 38 {
		t.Fatalf("free-run lines = %d, want ≈40", lines)
	}
	if prev == nil {
		t.Fatal("no preview")
	}
	if snap := d.Snapshot(); snap == nil || snap.Bounds().Dy() != lines {
		t.Fatal("snapshot mismatch")
	}
}

// Manual mode (the default): APT tones must NOT finish or reset —
// reception continues across charts until the user saves.
func TestWefaxManualContinuous(t *testing.T) {
	const spl = WefaxRate * 60 / WefaxLPM
	s := &wefaxSynth{}
	s.tone(1, WefaxRate/2)
	s.apt(300, 4)
	for l := 0; l < 30; l++ {
		s.tone(1, spl*5/100)
		s.tone(0, spl-spl*5/100)
	}
	for l := 0; l < 150; l++ {
		for i := 0; i < spl; i++ {
			s.tone(barAt(float64(i)/spl), 1)
		}
	}
	s.apt(450, 4) // stop tone
	s.tone(1, WefaxRate) // inter-chart white
	s.apt(300, 4)        // next chart starts
	for l := 0; l < 30; l++ {
		s.tone(1, spl*5/100)
		s.tone(0, spl-spl*5/100)
	}
	for l := 0; l < 100; l++ {
		for i := 0; i < spl; i++ {
			s.tone(barAt(float64(i)/spl), 1)
		}
	}
	d := NewWefaxDecoder() // autoSave defaults OFF
	d.Feed(s.out)
	if imgs := d.TakeDone(); len(imgs) != 0 {
		t.Fatalf("manual mode completed %d images, want 0", len(imgs))
	}
	_, lines, st := d.Preview(200, 100)
	if st != WefaxImage {
		t.Fatalf("state = %d, want Image (receiving)", st)
	}
	// 150 + 100 chart lines; tones/phasing lines are skipped; allow
	// slack for skipped/duplicated edges but it must keep growing
	// well past the first chart (>= 230 lines total).
	if lines < 230 {
		t.Fatalf("lines = %d, want >= 230 (reception continued past the stop tone)", lines)
	}
	// The alignment stays locked in Image via the phasing pulses:
	// check the second chart's rows against the bar pattern.
	img := d.Snapshot()
	if img == nil {
		t.Fatal("no snapshot")
	}
	W, H := img.Bounds().Dx(), img.Bounds().Dy()
	good, total := 0, 0
	for y := H - 90; y < H-6; y++ {
		for x := int(float64(W) * 0.06); x < int(float64(W)*0.94); x += 3 {
			xf := float64(x) / float64(W)
			u := math.Mod(xf*8, 1)
			if u < 0.08 || u > 0.92 {
				continue
			}
			want := barAt(xf)
			total++
			if math.Abs(float64(img.GrayAt(x, y).Y)/255-want) < 0.35 {
				good++
			}
		}
	}
	if acc := float64(good) / float64(total); acc < 0.9 {
		t.Fatalf("second chart alignment accuracy %.3f, want >=0.9", acc)
	}
}

// A weak signal demodulates to noisy luma that crosses 0.5 often and
// irregularly. It must never look like an APT tone (the old edge-count
// detector false-triggered here and killed reception mid-chart).
func TestWefaxNoiseIsNotAPT(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	d := NewWefaxDecoder()
	d.mu.Lock()
	d.state = WefaxImage
	d.mu.Unlock()
	for i := 0; i < WefaxRate*10; i++ { // 10 s of pure noise
		d.Feed([]float64{rng.NormFloat64() * 0.4})
	}
	d.mu.Lock()
	latch, lines, st := d.toneLatch, d.lines, d.state
	done := len(d.done)
	d.mu.Unlock()
	if latch != 0 {
		t.Fatalf("noise latched APT tone %d Hz", latch)
	}
	if done != 0 {
		t.Fatalf("noise finished %d images", done)
	}
	if st != WefaxImage || lines == 0 {
		t.Fatalf("state=%d lines=%d — reception must continue", st, lines)
	}
}
