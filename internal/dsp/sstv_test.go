package dsp

import (
	"image/color"
	"math"
	"testing"
)

// sstvSynth renders one frame of a mode as 8 kHz audio: leader, break,
// leader, break, VIS, then every line's segment sequence with the
// given per-line colour gradient.
func sstvSynth(m SSTVMode, lines int) []float64 {
	var out []float64
	ph := 0.0
	tone := func(ms float64, hz float64) {
		n := int(ms * 8)
		for i := 0; i < n; i++ {
			ph += 2 * math.Pi * hz / 8000
			out = append(out, 0.6*math.Sin(ph))
		}
	}
	tone(300, 1900) // leader
	tone(30, 1200)  // break
	tone(300, 1900) // leader 2
	tone(30, 1200)  // break 2 = VIS start bit
	// VIS: start(0)=1200, bits LSB-first, parity, stop(1)=1900
	vis := m.VIS
	par := 0
	bit := func(b byte) {
		if b == 1 {
			tone(30, 1300)
			par ^= 1
		} else {
			tone(30, 1100)
		}
	}
	// the 30 ms break above WAS the start bit; now the 7 data bits
	for i := 0; i < 7; i++ {
		bit((vis >> i) & 1)
	}
	bit(byte(par))
	bit(1)
	// lines
	for y := 0; y < lines; y++ {
		pos := 0.0
		for _, sg := range m.Seg {
			n := int(sg.DurMs * 8)
			switch sg.Kind {
			case 0:
				if pos == 0 {
					tone(sg.DurMs, 1200) // line sync
				} else {
					tone(sg.DurMs, 1500) // porch/separator = black level
				}
			default:
				ch := sg.Kind - 1
				// Fractional samples per pixel (3.66 at M1): accumulate
				// exactly, or the line comes out 20% short of the real
				// protocol and the decoder reads it misaligned.
				acc := 0.0
				for p := 0; p < m.Width; p++ {
					v := []float64{40, 120, 200}[ch]
					if p < 16 {
						v = 255
					}
					acc += m.PixelSec * 8000
					n := int(acc)
					acc -= float64(n)
					toneMs := float64(n) / 8
					tone(toneMs, 1500+800*v/255)
				}
			}
			pos += float64(n)
		}
		_ = pos
	}
	tone(300, 1900) // end leader
	return out
}

func TestSSTVMartinM1Raster(t *testing.T) {
	d := NewSSTVDecoder()
	d.SetEnabled(true)
	audio := sstvSynth(martinM1, 40) // 40 of 256 lines
	const chunk = 1024
	for off := 0; off < len(audio); off += chunk {
		end := off + chunk
		if end > len(audio) {
			end = len(audio)
		}
		d.Feed(audio[off:end])
	}
	img, name, line, total, _ := d.Snapshot(-1)
	if name != "Martin M1" {
		t.Fatalf("VIS name = %q", name)
	}
	if img == nil {
		t.Fatalf("no image; line=%d/%d", line, total)
	}
	if line < 30 {
		t.Fatalf("only %d lines rastered of %d fed", line, 40)
	}
	// Alignment: the white marker must land at x<16 and the flat
	// levels (R200 G40 B120) fill the rest of the line.
	mk := img.NRGBAAt(8, 0)
	if mk.R < 230 || mk.G < 230 || mk.B < 230 {
		t.Errorf("marker at (8,0) = %+v, want white", mk)
	}
	flat := img.NRGBAAt(200, 0)
	if flat.R < 170 || flat.R > 230 || flat.G < 15 || flat.G > 70 || flat.B < 90 || flat.B > 150 {
		t.Errorf("flat at (200,0) = %+v, want ~(200,40,120)", flat)
	}
	_ = color.NRGBA{}
}

func TestSSTVScottieS1Vis(t *testing.T) {
	d := NewSSTVDecoder()
	d.SetEnabled(true)
	audio := sstvSynth(scottieS1, 10)
	d.Feed(audio)
	_, name, _, _, _ := d.Snapshot(-1)
	if name != "Scottie S1" {
		t.Fatalf("VIS = %q", name)
	}
}

func TestSSTVIgnoresNoise(t *testing.T) {
	d := NewSSTVDecoder()
	d.SetEnabled(true)
	rng := noiseSrc(5)
	blk := make([]float64, 8000)
	for i := range blk {
		blk[i] = 0.2 * rng.NormFloat64()
	}
	for i := 0; i < 10; i++ {
		d.Feed(blk)
	}
	img, _, line, _, _ := d.Snapshot(-1)
	if img != nil || line != 0 {
		t.Fatal("noise produced an image")
	}
}

func noiseSrc(seed int64) *randSrc { return &randSrc{seed} }

type randSrc struct{ s int64 }

func (r *randSrc) NormFloat64() float64 {
	// deterministic LCG-based pseudo-gaussian (good enough for noise)
	r.s = r.s*6364136223846793005 + 1442695040888963407
	x := float64(int64(r.s)>>11) / 9007199254740992.0
	r.s = r.s*6364136223846793005 + 1442695040888963407
	y := float64(int64(r.s)>>11) / 9007199254740992.0
	return math.Sqrt(-2*math.Log(x+1e-9)) * math.Cos(2*math.Pi*y)
}
