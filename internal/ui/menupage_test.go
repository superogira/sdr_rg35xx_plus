package ui

import (
	"image/png"
	"os"
	"testing"

	"sdr35/internal/i18n"
)

func TestMenuPagesRender(t *testing.T) {
	for _, page := range []struct {
		name  string
		items []MenuItem
	}{
		{"root", []MenuItem{
			{Label: i18n.T("m_rxpage"), Value: "▸"},
			{Label: i18n.T("m_ft8page"), Value: "▸"},
			{Label: i18n.T("m_syspage"), Value: "▸"},
		}},
		{"ft8", []MenuItem{
			{Label: i18n.T("m_ft8"), Value: "On"},
			{Label: i18n.T("m_call"), Value: "HS0ZKO"},
			{Label: i18n.T("m_grid"), Value: "OK04"},
			{Label: i18n.T("m_psk"), Value: "On"},
		}},
	} {
		u := New(640, 480)
		frame := u.Frame(FrameStats{FreqHz: 21074000, Mode: "USB"})
		u.DrawMenu(page.items, 0, "v20260923")
		if os.Getenv("SDR_PAGESHOT") != "" {
			f, err := os.Create("menu_" + page.name + ".png")
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, frame)
			f.Close()
		}
	}
}
