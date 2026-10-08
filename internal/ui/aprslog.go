// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import (
	"fmt"
	"time"

	"image/color"
)

// APRSStationUI is one live station row for the APRS log screen.
type APRSStationUI struct {
	Call     string
	Country  string // ISO-2 for the flag
	Sym      string // sender's symbol char
	Lat, Lon float64
	SpeedKt  float64
	AltFt    int
	Comment  string
	AgeSec   float64
}

// APRSLogUI is one history row (receive or transmit).
type APRSLogUI struct {
	Time    string
	Call    string
	Country string
	Sym     string
	Pos     string
	Extra   string // speed/alt or comment
	Via     string // RF / IS
}

// DrawAPRSLog renders the three-tab APRS screen: 0 stations, 1 receive
// history, 2 transmit history. L1/R1 switch tabs on the device.
func (u *UI) DrawAPRSLog(tab int, stations []APRSStationUI, rx, tx []APRSLogUI, scroll int, flagDir string) {
	lh := 16
	x, y := 12, 8
	w := u.W - 24
	h := u.WaterfallRows - 16
	if h < 4*lh {
		return
	}
	u.fillBlend(x, y, w, h, 0, 0, 0, 215)
	u.fillBlend(x, y, w, 2, 255, 90, 255, 255)
	u.fillBlend(x, y+h-2, w, 2, 255, 90, 255, 255)

	titleF := Face(14, true)
	lineF := Face(13, false)
	gray := color.RGBA{150, 180, 150, 255}
	white := color.RGBA{235, 235, 235, 255}
	mag := color.RGBA{255, 140, 255, 255}

	tabs := []string{"STN", "RX", "TX"}
	var entries int
	switch tab {
	case 0:
		entries = len(stations)
	case 1:
		entries = len(rx)
	default:
		entries = len(tx)
	}
	// Tab header
	tx0 := x + 10
	for i, t := range tabs {
		col := gray
		if i == tab {
			col = mag
			u.fillBlend(tx0-4, y+8, lineF.TextWidth(t)+8, lh, 60, 20, 60, 200)
		}
		lineF.DrawString(u.img, col, tx0, y+20, t)
		tx0 += lineF.TextWidth(t) + 22
	}
	title := fmt.Sprintf("APRS · %d", entries)
	titleF.DrawString(u.img, white, x+w-10-titleF.TextWidth(title), y+20, title)

	vis := (h - 2*lh - 14) / lh
	if vis < 1 {
		vis = 1
	}
	bottom := entries - scroll
	if bottom > entries {
		bottom = entries
	}
	if bottom < vis {
		bottom = vis
	}
	if bottom > entries {
		bottom = entries // short list: never index past it
	}
	top := bottom - vis
	if top < 0 {
		top = 0
	}
	pos := fmt.Sprintf("%d-%d", top+1, bottom)
	lineF.DrawString(u.img, gray, x+w-10-lineF.TextWidth(pos), y+lh+22, pos)

	if entries == 0 {
		lineF.DrawString(u.img, gray, x+10, y+lh+22+lh, "· · ·")
		return
	}
	for i := top; i < bottom; i++ {
		yy := y + 2*lh + 22 + (i-top)*lh
		switch tab {
		case 0:
			st := stations[i]
			if flag := GetFlag(st.Country, flagDir); flag != nil {
				drawImage(u.img, flag, x+10, yy-10)
			}
			lineF.DrawString(u.img, mag, x+38, yy, st.Call)
			lineF.DrawString(u.img, white, x+150, yy, fmt.Sprintf("%s %.4f,%.4f", st.Sym, st.Lat, st.Lon))
			extra := st.Comment
			if st.SpeedKt > 0.5 {
				extra = fmt.Sprintf("%.0fkt ", st.SpeedKt) + extra
			}
			lineF.DrawString(u.img, gray, x+330, yy, trim(extra, 26))
			lineF.DrawString(u.img, gray, x+w-70, yy, fmtAge(st.AgeSec))
		case 1, 2:
			var e APRSLogUI
			if tab == 1 {
				e = rx[i]
			} else {
				e = tx[i]
			}
			lineF.DrawString(u.img, gray, x+10, yy, e.Time)
			if flag := GetFlag(e.Country, flagDir); flag != nil {
				drawImage(u.img, flag, x+78, yy-10)
			}
			lineF.DrawString(u.img, mag, x+106, yy, e.Call)
			lineF.DrawString(u.img, white, x+210, yy, e.Pos)
			lineF.DrawString(u.img, gray, x+360, yy, trim(e.Extra, 20))
			via := color.RGBA{120, 200, 120, 255}
			if e.Via == "IS" {
				via = color.RGBA{120, 160, 255, 255}
			}
			lineF.DrawString(u.img, via, x+w-40, yy, e.Via)
		}
	}
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func fmtAge(sec float64) string {
	if sec < 60 {
		return fmt.Sprintf("%.0fs", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%.0fm", sec/60)
	}
	return fmt.Sprintf("%.1fh", sec/3600)
}

// nowStamp formats a log timestamp for APRSLogUI rows.
func NowStamp(t time.Time) string { return t.Format("15:04:05") }
