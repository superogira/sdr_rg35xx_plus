package main

import (
	"os"

	"sdr35/internal/ui"
)

func main() {
	u := ui.New(640, 480)
	img := u.Frame(ui.FrameStats{FreqHz: 1090000000, Mode: "AM"})
	u.DrawHostList([]string{"192.168.2.152:30005", "10.0.0.5:30005"}, 1, 0, "รายการ Beast Server")
	ui.SavePNG(os.TempDir()+string(os.PathSeparator)+"beastlist.png", img)
}
