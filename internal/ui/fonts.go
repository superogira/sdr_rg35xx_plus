package ui

import (
	_ "embed"
	"image"
	"image/color"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed fonts/Sarabun-Regular.ttf
var regularTTF []byte

//go:embed fonts/Sarabun-Bold.ttf
var boldTTF []byte

// FontFace is a parsed face plus its metrics for cheap text measuring.
type FontFace struct {
	face   font.Face
	height int
	ascent int
}

type fontSet struct {
	regular *opentype.Font
	bold    *opentype.Font
	faces   map[faceKey]*FontFace
}

type faceKey struct {
	bold bool
	size int
}

var fonts *fontSet

func init() {
	fs := &fontSet{faces: map[faceKey]*FontFace{}}
	fs.regular = mustParse(regularTTF)
	fs.bold = mustParse(boldTTF)
	fonts = fs
}

func mustParse(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic("font parse: " + err.Error())
	}
	return f
}

// Face returns a cached face. size is pixel height (DPI 72).
func Face(size int, bold bool) *FontFace {
	key := faceKey{bold: bold, size: size}
	if f, ok := fonts.faces[key]; ok {
		return f
	}
	src := fonts.regular
	if bold {
		src = fonts.bold
	}
	face, err := opentype.NewFace(src, &opentype.FaceOptions{
		Size:    float64(size),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		panic("face: " + err.Error())
	}
	m := face.Metrics()
	ff := &FontFace{
		face:   face,
		height: m.Height.Ceil(),
		ascent: m.Ascent.Ceil(),
	}
	fonts.faces[key] = ff
	return ff
}

// TextWidth returns the advance width of s in pixels.
func (f *FontFace) TextWidth(s string) int {
	d := &font.Drawer{Face: f.face}
	return d.MeasureString(s).Ceil()
}

// DrawString writes s with its baseline at (x, y).
func (f *FontFace) DrawString(dst *image.RGBA, c color.RGBA, x, y int, s string) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: f.face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}
