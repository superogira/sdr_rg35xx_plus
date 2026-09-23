package ui

import (
	
	"image/png"
	"os"
	"testing"

	"sdr35/internal/i18n"
)

// Renders the real 16-row menu and verifies the panel header/footer
// pixels land inside the framebuffer (a fixed row height once pushed
// the title above and the version footer below the 480 px screen).
func TestMenuHeaderFooterOnScreen(t *testing.T) {
	u := New(640, 480)
	items := make([]MenuItem, 16)
	for i := range items {
		items[i] = MenuItem{Label: "Item", Value: "999"}
	}
	frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
	u.DrawMenu(items, 3, "รุ่น 202609231405 test")

	// The menu title must have bright cyan pixels near the top band of
	// the screen (y < 60), and the footer grey pixels near the bottom
	// (y > H-40).
	cyanTop := false
	footerBottom := false
	for y := 0; y < 60 && !cyanTop; y++ {
		for x := 0; x < 640; x++ {
			r, g, b, _ := frame.At(x, y).RGBA()
			if r>>8 > 40 && g>>8 > 150 && b>>8 > 150 {
				cyanTop = true
				break
			}
		}
	}
	for y := 480 - 30; y < 480 && !footerBottom; y++ {
		for x := 0; x < 640; x++ {
			r, g, b, _ := frame.At(x, y).RGBA()
			if r>>8 > 80 && r>>8 < 200 && g>>8 > 90 && g>>8 < 200 && b>>8 > 100 {
				footerBottom = true
				break
			}
		}
	}
	if !cyanTop {
		t.Error("menu title not visible near the top of the screen")
	}
	if !footerBottom {
		t.Error("menu footer not visible near the bottom of the screen")
	}

	// Visual sanity: save a PNG only when SDR_MENUSHOT is set.
	if os.Getenv("SDR_MENUSHOT") != "" {
		f, _ := os.Create("menushort.png")
		defer f.Close()
		png.Encode(f, frame)
	}
	_ = i18n.T("menu_title")
}
