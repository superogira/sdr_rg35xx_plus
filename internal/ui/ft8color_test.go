package ui

import (
	"image/png"
	"os"
	"testing"
)

func TestFT8TextColor(t *testing.T) {
	cases := []struct {
		text string
		want color_t
	}{
		{"CQ BG0HP NL59", cq},
		{"CQ DX VU2KPH NL25", cq},
		{"CQ POTA BG7ZHS", cq},
		{"OT7K JA3OPL PM74", call},
		{"DW1VI JA6JQV PM53", call},
		{"F4CQS BG7ZHS R -20", rpt},
		{"IK7YZB VR2KF -17", rpt},
		{"EV1R JA3GAK R +00", rpt},
		{"IK4TVP YD1COX R -15", rpt},
		{"TF8KW BG0HP RR73", end},
		{"F5RRS JK1QHK RR73", end},
		{"EW8AAC JK1QHK 73", end},
		{"AA1AA BB2BB RRR", end},
	}
	for _, c := range cases {
		got := ft8TextColor(c.text)
		if got != wantColor(c.want) {
			t.Errorf("%q: got %v", c.text, got)
		}
	}
}

type color_t int

const (
	cq color_t = iota
	call
	rpt
	end
)

func wantColor(k color_t) (c interface{ RGBA() (r, g, b, a uint32) }) {
	switch k {
	case cq:
		return ft8ColorCQ
	case call:
		return ft8ColorCall
	case end:
		return ft8ColorEnd
	}
	return ft8ColorRpt
}

func TestFT8ColorsRender(t *testing.T) {
	u := New(640, 480)
	entries := []FT8Entry{
		{Time: "21:15:03", SNRDb: 9, Text: "CQ BG0HP NL59"},
		{Time: "21:15:03", SNRDb: 7, Text: "OT7K JA3OPL PM74"},
		{Time: "21:15:18", SNRDb: 4, Text: "F4CQS BG7ZHS R -20"},
		{Time: "21:15:33", SNRDb: 5, Text: "TF8KW BG0HP RR73"},
	}
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	u.DrawFT8Log(entries)
	if os.Getenv("SDR_COLORSHOT") != "" {
		f, _ := os.Create("ft8colors.png")
		defer f.Close()
		png.Encode(f, frame)
	}
}

func TestFT8ColorsPixels(t *testing.T) {
	u := New(640, 480)
	entries := []FT8Entry{
		{Time: "21:15:03", SNRDb: 9, Text: "CQ BG0HP NL59"},
		{Time: "21:15:03", SNRDb: 7, Text: "OT7K JA3OPL PM74"},
		{Time: "21:15:18", SNRDb: 4, Text: "F4CQS BG7ZHS R -20"},
		{Time: "21:15:33", SNRDb: 5, Text: "TF8KW BG0HP RR73"},
	}
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	u.DrawFT8Log(entries)
	has := func(match func(r, g, b int) bool) bool {
		for y := 0; y < 480; y++ {
			for x := 0; x < 340; x++ {
				r, g, b, _ := frame.At(x, y).RGBA()
				if match(int(r>>8), int(g>>8), int(b>>8)) {
					return true
				}
			}
		}
		return false
	}
	// red (CQ): R high, G/B low
	if !has(func(r, g, b int) bool { return r > 200 && g < 140 && b < 140 }) {
		t.Error("no red CQ text pixels")
	}
	// orange: R and G high, B low
	if !has(func(r, g, b int) bool { return r > 200 && g > 130 && g < 210 && b < 120 }) {
		t.Error("no orange directed-call text pixels")
	}
	// green: G dominant
	if !has(func(r, g, b int) bool { return g > 200 && r < 160 }) {
		t.Error("no green report text pixels")
	}
	// blue: B high, R low-ish
	if !has(func(r, g, b int) bool { return b > 200 && r < 170 }) {
		t.Error("no blue sign-off text pixels")
	}
}
