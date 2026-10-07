// Package web exposes the receiver as a small LAN web service: a JSON
// control/state API plus a self-contained page (embedded HTML/JS) that
// renders and controls the radio from any browser on the network.
// Everything is stdlib — the handheld has no room for dependencies.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"math"
	"math/bits"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"sdr35/internal/adsb"
	"sdr35/internal/ais"
	"sdr35/internal/dsp"
	"sdr35/internal/geo"
	"sdr35/internal/i18n"
	"sdr35/internal/osm"
	"sdr35/internal/radio"
	"sdr35/internal/sysinfo"
	"sdr35/internal/ui"
)

//go:embed index.html
//go:embed static/leaflet.js static/leaflet.css
var page embed.FS

// FT8Line is one decoded FT8 message for the web tables.
type FT8Line struct {
	Time string  `json:"time"`
	SNR  float64 `json:"snr"`
	Hz   float64 `json:"hz"`
	Text string  `json:"text"`
	Anno string  `json:"anno"`
}

// AISLine is one decoded AIS message (RF or NMEA) for the web tables.
type AISLine struct {
	Time string  `json:"time"`
	Ch   string  `json:"ch"`
	Text string  `json:"text"`
	Db   float64 `json:"db"`
}

// Server owns the HTTP listener and the log mirrors.
// sysSample is one system-metrics history point.
type sysSample struct {
	t          int64
	cpu, mem   float64
	bat        int
	ct, gt, dt float64
}

// sysHistCap is 24 h at the 10 s sample interval.
const sysHistCap = 24 * 3600 / 10

type Server struct {
	mu      sync.Mutex
	enabled bool
	port    int
	srv     *http.Server

	radio    *radio.Radio
	adsb     *adsb.Store
	ais      *ais.Store
	tiles    *osm.Cache
	rx       [2]float64
	audio    *audioHub
	noise    float64
	rebootAt time.Time
	updMsg   func() string
	updRun   func()
	ft8Log   []FT8Line
	aisLog   []AISLine
	logMu    sync.Mutex
	upSince  time.Time

	// system metrics history (since app start; cleared on exit by
	// process death — nothing is persisted)
	histMu sync.Mutex
	hist   []sysSample

	// cmdMu serializes handleCmd: web commands run one at a time so
	// no two HTTP goroutines can race on anything they touch.
	cmdMu sync.Mutex

	// WEFAX manual-save hook (main wires it so the file lands next to
	// the binary exactly like the device's Y-save; "" = nothing yet).
	wfSave func() string

	// Waterfall display range shared with the device menu (wfmin/
	// wfmax ini): get returns the live values, set applies a change
	// (device UI + cfg + saveNow). Nil until main wires them.
	wfGet    func() (float64, float64)
	rxPos    func() (float64, float64)
	gpsGet   func() GPSInfo
	aprsGet  func() []APRSStation
	aprsCmd  func(action string, v int) string
	aprsStat func() (rx bool, beac int, isOn bool, igate bool, src string, count int)
	extraTgt func() []Target
	aprsLog  func() APRSLog
	ft8Grid  func([]FT8Line) []FT8MapEntry
	panelSet func(state int)
	panelGet func() int
	wfSet    func(min, max float64)

	// Spec scratch reused across /api/spec calls (FFT work arrays at
	// specMaxFFT; per-call allocation would churn ~3 MB at 8 Hz).
	specMu   sync.Mutex
	specBuf  []complex128
	specRe   []float64
	specIm   []float64
	specPow  []float64
	specSort []float64
}

// New builds a (not yet listening) server.
// SetUpdater wires the OTA update hooks: msg returns the live status
// text ("" when idle), run starts a manual check+install.
func (s *Server) SetUpdater(msg func() string, run func()) {
	s.mu.Lock()
	s.updMsg, s.updRun = msg, run
	s.mu.Unlock()
}

// SetWaterfallRange wires the shared waterfall display range. The
// web sliders and the device menu stay in sync through these hooks.
func (s *Server) SetWaterfallRange(get func() (float64, float64), set func(min, max float64)) {
	s.mu.Lock()
	s.wfGet, s.wfSet = get, set
	s.mu.Unlock()
}

// SetRxPosFunc wires the live receiver position (GPS follow overrides
// the ini lat/lon; state and the radar center track it).
func (s *Server) SetRxPosFunc(f func() (float64, float64)) {
	s.mu.Lock()
	s.rxPos = f
	s.mu.Unlock()
}

// GPSInfo is the live GPS snapshot served to the web System card.
type GPSInfo struct {
	Device   string  `json:"device"`
	Valid    bool    `json:"valid"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	Alt      float64 `json:"alt"`
	SpeedKt  float64 `json:"speedKt"`
	Course   float64 `json:"course"`
	SatsUsed int     `json:"satsUsed"`
	SatsView int     `json:"satsView"`
	HDOP     float64 `json:"hdop"`
	Grid     string  `json:"grid"`
	AgeSec   float64 `json:"ageSec"`
	Follow   bool    `json:"follow"`
	TimeUTC  string  `json:"timeUTC"`
	DateUTC  string  `json:"dateUTC"`
}

// SetGPSProvider wires the live GPS snapshot for the System card and
// the GPS detail window.
// APRSStation is one decoded APRS station for the web map/table.
type APRSStation struct {
	Call    string  `json:"call"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	SpeedKt float64 `json:"speedKt"`
	Course  float64 `json:"course"`
	AltFt   int     `json:"altFt"`
	Comment string  `json:"comment"`
	AgeSec  float64 `json:"ageSec"`
	Country string  `json:"country"` // ISO-2 for the flag
	Sym     string  `json:"sym"`     // emoji per the sender's symbol
	SymChar string  `json:"symChar"` // raw APRS symbol char
	Own     bool    `json:"own"`     // our own transmitted position
}

// APRSLog bundles the three APRS histories for the web tab.
type APRSLog struct {
	Stations []APRSStation `json:"stations"`
	Rx       []APRSLogRow  `json:"rx"`
	Tx       []APRSLogRow  `json:"tx"`
}

// APRSLogRow is one history entry.
type APRSLogRow struct {
	At      int64   `json:"at"` // unix seconds
	Call    string  `json:"call"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	SpeedKt float64 `json:"speedKt"`
	AltFt   int     `json:"altFt"`
	Comment string  `json:"comment"`
	Info    string  `json:"info"`
	Via     string  `json:"via"`
}

// SetAPRSProvider hands the web server the live APRS station list.
func (s *Server) SetAPRSProvider(f func() []APRSStation) {
	s.mu.Lock()
	s.aprsGet = f
	s.mu.Unlock()
}

// SetFT8Grid computes FT8 map entries (stations + QSO arcs) from the
// decoded log — the same recipe as the handheld map screen.
func (s *Server) SetFT8Grid(f func([]FT8Line) []FT8MapEntry) {
	s.mu.Lock()
	s.ft8Grid = f
	s.mu.Unlock()
}

// SetAPRSLog hands the web server the APRS histories.
func (s *Server) SetAPRSLog(f func() APRSLog) {
	s.mu.Lock()
	s.aprsLog = f
	s.mu.Unlock()
}

// SetExtraTargets lets main append synthetic targets (dev demo with
// trails) to /api/targets.
func (s *Server) SetExtraTargets(f func() []Target) {
	s.mu.Lock()
	s.extraTgt = f
	s.mu.Unlock()
}

// SetAPRSState hands the web server the live APRS config snapshot.
func (s *Server) SetAPRSState(f func() (rx bool, beac int, isOn bool, igate bool, src string, count int)) {
	s.mu.Lock()
	s.aprsStat = f
	s.mu.Unlock()
}

// SetAPRSCmd hands the web server the APRS control entry point
// (action: "rx"/"beacon"/"is"/"now"; v is the mode index or 0/1).
func (s *Server) SetAPRSCmd(f func(action string, v int) string) {
	s.mu.Lock()
	s.aprsCmd = f
	s.mu.Unlock()
}

func (s *Server) SetGPSProvider(f func() GPSInfo) {
	s.mu.Lock()
	s.gpsGet = f
	s.mu.Unlock()
}

// SetPanelStateFunc exposes the live panel state (0/1/2) for the web
// buttons' active highlight.
func (s *Server) SetPanelStateFunc(f func() int) {
	s.mu.Lock()
	s.panelGet = f
	s.mu.Unlock()
}

// SetPanelFunc wires the web panel buttons (screen on/dim/off). The
// callback runs on the cmd handler goroutine; the app posts the actual
// state change to its UI loop.
func (s *Server) SetPanelFunc(f func(state int)) {
	s.mu.Lock()
	s.panelSet = f
	s.mu.Unlock()
}

// SetWefaxSaver wires the manual WEFAX snapshot saver.
func (s *Server) SetWefaxSaver(f func() string) {
	s.mu.Lock()
	s.wfSave = f
	s.mu.Unlock()
}

func New(r *radio.Radio, adsbStore *adsb.Store, aisStore *ais.Store, port int, tiles *osm.Cache, rxLat, rxLon float64) *Server {
	srv := &Server{radio: r, adsb: adsbStore, ais: aisStore, port: port, tiles: tiles, rx: [2]float64{rxLat, rxLon}, upSince: time.Now()}
	srv.audio = newAudioHub()
	srv.updMsg = func() string { return "" }
	r.SetAudioTap(srv.audio.push)
	return srv
}

// ResetFT8 clears the web FT8 log (demo seeding rebuilds it fresh).
func (s *Server) ResetFT8() {
	s.logMu.Lock()
	s.ft8Log = nil
	s.logMu.Unlock()
}

// AddFT8 mirrors one decode into the web log (called from the DSP
// callback goroutine; mutex-guarded unlike the on-device window list).
func (s *Server) AddFT8(l FT8Line) {
	s.logMu.Lock()
	s.ft8Log = append(s.ft8Log, l)
	if len(s.ft8Log) > 200 {
		s.ft8Log = s.ft8Log[len(s.ft8Log)-200:]
	}
	s.logMu.Unlock()
}

// AddAIS mirrors one AIS decode.
func (s *Server) AddAIS(l AISLine) {
	s.logMu.Lock()
	s.aisLog = append(s.aisLog, l)
	if len(s.aisLog) > 200 {
		s.aisLog = s.aisLog[len(s.aisLog)-200:]
	}
	s.logMu.Unlock()
}

// SetEnabled starts or stops the listener.
func (s *Server) SetEnabled(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on == s.enabled {
		return
	}
	s.enabled = on
	if on {
		s.startLocked()
	} else {
		s.stopLocked()
	}
}

// SetPort retargets the listener (restarts it when running).
func (s *Server) SetPort(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port == port {
		return
	}
	s.port = port
	if s.enabled {
		s.stopLocked()
		s.startLocked()
	}
}

// Port reports the configured port.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Enabled reports whether the listener is up.
func (s *Server) Enabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled
}

func (s *Server) startLocked() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePage)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/cmd", s.handleCmd)
	mux.HandleFunc("/api/ft8", s.handleFT8)
	mux.HandleFunc("/api/ais", s.handleAIS)
	mux.HandleFunc("/api/targets", s.handleTargets)
	mux.HandleFunc("/api/aprslog", s.handleAPRSLog)
	mux.HandleFunc("/api/syshist", s.handleSysHist)
	mux.HandleFunc("/api/ft8map", s.handleFT8Map)
	mux.HandleFunc("/api/flag/", s.handleFlag)
	mux.HandleFunc("/api/spec", s.handleSpec)
	mux.HandleFunc("/api/wefaximg", s.handleWefaxImg)
	mux.HandleFunc("/api/sstvimg", s.handleSSTVImg)
	mux.HandleFunc("/api/layers", s.handleLayers)
	mux.HandleFunc("/api/audio", s.handleAudio)
	mux.HandleFunc("/tiles/", s.handleTile)
	staticSub, err := fs.Sub(page, "static")
	if err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	}
	s.srv = &http.Server{Addr: fmt.Sprintf(":%d", s.port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go s.sampleLoop()
	go s.srv.ListenAndServe() //nolint:errcheck // listener errors surface as "unreachable" in the UI
	fmt.Fprintf(os.Stderr, "web: server listening on :%d\n", s.port)
}

func (s *Server) stopLocked() {
	if s.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s.srv.Shutdown(ctx) //nolint:errcheck
		cancel()
		s.srv = nil
	}
	fmt.Fprintf(os.Stderr, "web: server stopped\n")
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	b, _ := page.ReadFile("index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// state is the full control surface the page renders.
type state struct {
	FreqHz    int64     `json:"freqHz"`
	LOHz      int64     `json:"loHz"`
	Mode      string    `json:"mode"`
	Modes     []string  `json:"modes"`
	GainDb    float64   `json:"gainDb"`
	AGC       bool      `json:"agc"`
	Vol       float64   `json:"vol"`
	SqlDb     float64   `json:"sqlDb"`
	AprsRx    bool      `json:"aprsRx"`
	AprsBeac  int       `json:"aprsBeac"` // 0 off, 1..5 minutes idx, 6 smart
	AprsIS    bool      `json:"aprsIs"`
	AprsIgate bool      `json:"aprsIgate"`
	AprsSrc   string    `json:"aprsSrc"`
	AprsCount int       `json:"aprsCount"`
	BwHz      float64   `json:"bwHz"`
	Bws       []float64 `json:"bws"`
	Ppm       int       `json:"ppm"`
	PpmOff    bool      `json:"ppmOff"`
	FT8       bool      `json:"ft8"`
	SSTV      bool      `json:"sstv"`
	AISRF     bool      `json:"aisrf"`
	LocalMute bool      `json:"localmute"`
	Reboot    bool      `json:"reboot"`
	IQRate    int       `json:"iqRate"`
	Rates     []int     `json:"rates"`
	Connected bool      `json:"connected"`
	Host      string    `json:"host"`
	CPU       float64   `json:"cpu"`
	MEM       float64   `json:"mem"`
	BAT       int       `json:"bat"`
	BATChg    bool      `json:"batCharging"`
	Panel     int       `json:"panel"`
	Planes    int       `json:"planes"`
	Ships     int       `json:"ships"`
	UpSecs    int       `json:"upSecs"`
	RxLat     float64   `json:"rxLat"`
	RxLon     float64   `json:"rxLon"`
	GPS       *GPSInfo  `json:"gps"`
	CPUTemp   float64   `json:"cpuTemp"`
	GPUTemp   float64   `json:"gpuTemp"`
	DDRTemp   float64   `json:"ddrTemp"`
	Updating  string    `json:"updating"`
	Lang      string    `json:"lang"`
	WfMin     float64   `json:"wfMin"`
	WfMax     float64   `json:"wfMax"`
	Wefax     bool      `json:"wefax"`
	Nr        int       `json:"nr"`
	Hp        int       `json:"hp"`
	Lp        int       `json:"lp"`
	WefaxAuto bool      `json:"wefaxAuto"`
	WefaxLn   int       `json:"wefaxLines"`
	WefaxSt   string    `json:"wefaxState"`
	CW        bool      `json:"cw"`
	CWText    string    `json:"cwText"`
	CWWpm     float64   `json:"cwWpm"`
}

func (s *Server) updateMsg() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updMsg()
}

// rebooting reports the window right after a web-triggered update in
// which the process re-execs — the page reloads once it ends.
func (s *Server) rebooting() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.rebootAt) < 90*time.Second
}

// sampleLoop records system metrics every 10 s for the history graph.
func (s *Server) sampleLoop() {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for range tick.C {
		cpu, mem, _ := sysinfo.Snapshot()
		sens := sysinfo.SensorSnapshot()
		s.histMu.Lock()
		s.hist = append(s.hist, sysSample{
			t: time.Now().Unix(), cpu: cpu, mem: mem, bat: sens.BattPct,
			ct: sens.CPUTemp, gt: sens.GPUTemp, dt: sens.DDRTemp,
		})
		if len(s.hist) > sysHistCap {
			s.hist = s.hist[len(s.hist)-sysHistCap:]
		}
		s.histMu.Unlock()
	}
}

func (s *Server) handleSysHist(w http.ResponseWriter, r *http.Request) {
	s.histMu.Lock()
	n := len(s.hist)
	out := struct {
		T   []int64   `json:"t"`
		Cpu []float64 `json:"cpu"`
		Mem []float64 `json:"mem"`
		Bat []float64 `json:"bat"`
		Ct  []float64 `json:"ct"`
		Gt  []float64 `json:"gt"`
		Dt  []float64 `json:"dt"`
	}{
		T: make([]int64, n), Cpu: make([]float64, n), Mem: make([]float64, n),
		Bat: make([]float64, n), Ct: make([]float64, n), Gt: make([]float64, n), Dt: make([]float64, n),
	}
	for i, h := range s.hist {
		out.T[i] = h.t
		out.Cpu[i] = math.Round(h.cpu*10) / 10
		out.Mem[i] = math.Round(h.mem*10) / 10
		out.Bat[i] = float64(h.bat)
		out.Ct[i] = math.Round(h.ct*10) / 10
		out.Gt[i] = math.Round(h.gt*10) / 10
		out.Dt[i] = math.Round(h.dt*10) / 10
	}
	s.histMu.Unlock()
	writeJSON(w, out)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	radio := s.radio
	snap := radio.Snapshot()
	cpu, mem, _ := sysinfo.Snapshot()
	sens := sysinfo.SensorSnapshot()
	st := state{
		FreqHz: radio.Freq(), LOHz: radio.LO(), Mode: radio.Mode().Name,
		GainDb: radio.GainDb(), AGC: radio.AGCEnabled(), Vol: radio.Volume(),
		SqlDb: radio.SquelchDb(), BwHz: radio.Bandwidth(), Bws: radio.Bandwidths(),
		Ppm: radio.Ppm(), PpmOff: radio.PpmOff(),
		FT8: radio.FT8Enabled(), AISRF: radio.AISRFEnabled(), LocalMute: radio.LocalMuted(), SSTV: radio.SSTVEnabled(),
		IQRate: radio.IQRate(), Rates: dsp.SampleRates,
		Reboot:    s.rebooting(),
		Connected: snap.Connected, Host: radio.Hostname(),
		CPU: cpu, MEM: mem, BAT: sens.BattPct,
		BATChg: strings.Contains(sens.BattStatus, "harg"),
		Planes: s.adsb.CountLive(), Ships: len(s.ais.Ships()),
		UpSecs: int(time.Since(s.upSince).Seconds()), Lang: i18n.Lang(),
		WfMin: 6, WfMax: 62,
		RxLat: s.rx[0], RxLon: s.rx[1],
		CPUTemp: sens.CPUTemp, GPUTemp: sens.GPUTemp, DDRTemp: sens.DDRTemp,
		Updating: s.updateMsg(),
	}
	s.mu.Lock()
	stat := s.aprsStat
	s.mu.Unlock()
	if stat != nil {
		rx, beac, isOn, igate, src, count := stat()
		st.AprsRx, st.AprsBeac, st.AprsIS, st.AprsIgate, st.AprsSrc, st.AprsCount = rx, beac, isOn, igate, src, count
	}
	for _, m := range dsp.ModeList {
		st.Modes = append(st.Modes, m.Name)
	}
	s.mu.Lock()
	wfGet := s.wfGet
	rxPos := s.rxPos
	s.mu.Unlock()
	if rxPos != nil {
		st.RxLat, st.RxLon = rxPos()
	}
	if wfGet != nil {
		if mn, mx := wfGet(); mn > 0 || mx > 0 {
			st.WfMin, st.WfMax = mn, mx
		}
	}
	s.mu.Lock()
	gpsGet := s.gpsGet
	panelGet := s.panelGet
	s.mu.Unlock()
	if panelGet != nil {
		st.Panel = panelGet()
	}
	if gpsGet != nil {
		g := gpsGet()
		st.GPS = &g
	}
	st.Wefax, st.WefaxAuto = radio.WefaxEnabled(), radio.WefaxAutoSave()
	st.Nr = radio.NoiseReduction()
	st.Hp, st.Lp = radio.AudioFilter()
	st.CW = radio.CWDecodeEnabled()
	if st.CW {
		st.CWText, st.CWWpm = radio.CW().Text(), radio.CW().WPM()
	}
	if st.Wefax {
		if ln, ws := radio.Wefax().Stats(); true {
			st.WefaxLn = ln
			switch ws {
			case dsp.WefaxPhasing:
				st.WefaxSt = "phasing"
			case dsp.WefaxImage:
				st.WefaxSt = "rx"
			default:
				st.WefaxSt = "wait"
			}
		}
	}
	writeJSON(w, st)
}

type cmd struct {
	Cmd  string  `json:"cmd"`
	Hz   float64 `json:"hz"`
	Db   float64 `json:"db"`
	V    float64 `json:"v"`
	Name string  `json:"name"`
	On   *bool   `json:"on"`
}

func (s *Server) handleCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	var c cmd
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	radio := s.radio
	switch c.Cmd {
	case "freq":
		radio.SetFreq(int64(c.Hz))
	case "offset":
		radio.SetFreq(radio.LO() + int64(c.Hz))
	case "mode":
		if m := dsp.ModeByName(c.Name); m.Name != "" {
			if key := radio.SetMode(m); key != "" {
				// FT8/AIS RF hold the mode — tell the page WHY instead
				// of letting the dropdown silently snap back.
				writeJSON(w, map[string]interface{}{"ok": false, "err": key})
				return
			}
		}
	case "gain":
		radio.SetGainDb(c.Db)
	case "agc":
		if c.On != nil {
			radio.SetAGCEnabled(*c.On)
		}
	case "vol":
		radio.SetVolume(c.V)
	case "sql":
		radio.SetSquelchDb(c.Db)
	case "bw":
		radio.SetBandwidth(c.Hz)
	case "ppm":
		radio.SetPpm(int(c.V))
	case "ppmoff":
		if c.On != nil {
			radio.SetPpmOff(*c.On)
		}
	case "ft8":
		if c.On != nil {
			radio.SetFT8Enabled(*c.On)
		}
	case "aisrf":
		if c.On != nil {
			radio.SetAISRFEnabled(*c.On)
		}
	case "wefax":
		if c.On != nil {
			radio.SetWefaxEnabled(*c.On)
		}
	case "wefaxauto":
		if c.On != nil {
			radio.SetWefaxAutoSave(*c.On)
		}
	case "wefaxclear":
		radio.Wefax().Clear()
	case "cwdec":
		if c.On != nil {
			radio.SetCWDecodeEnabled(*c.On)
		}
	case "cwclear":
		radio.CW().Clear()
	case "nr":
		radio.SetNoiseReduction(int(c.V))
	case "hp":
		radio.SetAudioFilter("hp", int(c.V))
	case "lp":
		radio.SetAudioFilter("lp", int(c.V))
	case "wefaxsave":
		s.mu.Lock()
		f := s.wfSave
		s.mu.Unlock()
		if f == nil {
			return
		}
		if path := f(); path == "" {
			writeJSON(w, map[string]interface{}{"ok": false, "err": "wefax_empty"})
			return
		} else {
			writeJSON(w, map[string]interface{}{"ok": true, "path": path})
			return
		}
	case "rate":
		radio.SetCaptureRate(int(c.V))
	case "localmute":
		if c.On != nil {
			radio.SetLocalMute(*c.On)
		}
	case "aprs":
		s.mu.Lock()
		fn := s.aprsCmd
		s.mu.Unlock()
		if fn == nil {
			writeJSON(w, map[string]any{"ok": false, "err": "aprs not wired"})
			return
		}
		action := c.Name
		v := int(c.V)
		msg := fn(action, v)
		writeJSON(w, map[string]any{"ok": true, "msg": msg})
		return
	case "sstv":
		if c.On != nil {
			radio.SetSSTVEnabled(*c.On)
		}
	case "wfxshift":
		if s.radio != nil && s.radio.WefaxEnabled() {
			s.radio.Wefax().Shift(float64(int(c.V)) / 100)
			writeJSON(w, map[string]any{"ok": true})
			return
		}
		writeJSON(w, map[string]any{"ok": false, "err": "wefax off"})
		return
	case "panel":
		// 0 = screen on, 1 = backlight dim, 2 = screen off (same states
		// as the device power key).
		s.mu.Lock()
		f := s.panelSet
		s.mu.Unlock()
		if f != nil {
			f(int(c.V))
		}
	case "update":
		s.mu.Lock()
		run := s.updRun
		s.rebootAt = time.Now()
		s.mu.Unlock()
		if run != nil {
			go run()
		}
	case "wfmin", "wfmax":
		s.mu.Lock()
		get, set := s.wfGet, s.wfSet
		s.mu.Unlock()
		if set == nil || get == nil {
			return
		}
		mn, mx := get()
		if c.Cmd == "wfmin" {
			mn = math.Max(0, math.Min(40, c.V))
		} else {
			mx = math.Max(10, math.Min(120, c.V))
		}
		set(mn, mx)
	default:
		http.Error(w, "unknown cmd", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleFT8(w http.ResponseWriter, r *http.Request) {
	s.logMu.Lock()
	out := make([]FT8Line, len(s.ft8Log))
	copy(out, s.ft8Log)
	s.logMu.Unlock()
	writeJSON(w, out)
}

func (s *Server) handleAIS(w http.ResponseWriter, r *http.Request) {
	s.logMu.Lock()
	out := make([]AISLine, len(s.aisLog))
	copy(out, s.aisLog)
	s.logMu.Unlock()
	writeJSON(w, out)
}

type Target struct {
	Kind    string    `json:"kind"` // "plane" | "ship"
	ID      string    `json:"id"`
	Call    string    `json:"call"`
	Lat     float64   `json:"lat"`
	Lon     float64   `json:"lon"`
	HasPos  bool      `json:"hasPos"`
	AltFt   int       `json:"altFt"`
	Speed   float64   `json:"speed"`
	Track   int       `json:"track"`
	AgeSec  float64   `json:"ageSec"`
	Country string    `json:"country"`
	Own     bool      `json:"own"`
	Trail   []TrailPt `json:"trail,omitempty"`
}

// trailPt is one breadcrumb of a flown path (altitude-coloured dots).
type TrailPt struct {
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	AltFt  int     `json:"altFt"`
	AgeSec float64 `json:"ageSec"`
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	var out []Target
	now := time.Now()
	for _, p := range s.adsb.Planes() {
		call := p.Callsign
		if call == "" {
			call = p.ICAO
		}
		t := Target{Kind: "plane", ID: p.ICAO, Call: call, Lat: p.Lat, Lon: p.Lon, HasPos: p.HasPos, AltFt: p.AltFt, Speed: float64(p.SpeedKt), Track: p.TrackDeg, AgeSec: now.Sub(p.LastSeen).Seconds(), Country: geo.ICAOCountry(p.ICAO)}
		for _, d := range p.Trail {
			t.Trail = append(t.Trail, TrailPt{Lat: d.Lat, Lon: d.Lon, AltFt: d.AltFt, AgeSec: now.Sub(d.At).Seconds()})
		}
		out = append(out, t)
	}
	for _, sh := range s.ais.Ships() {
		call := sh.Name
		if call == "" {
			call = sh.MMSI
		}
		t := Target{Kind: "ship", ID: sh.MMSI, Call: call, Lat: sh.Lat, Lon: sh.Lon, HasPos: sh.HasPos, Speed: sh.SogKt, Track: int(sh.CogDeg), AgeSec: now.Sub(sh.LastSeen).Seconds(), Country: geo.MMSICountry(sh.MMSI)}
		for _, d := range sh.Trail {
			t.Trail = append(t.Trail, TrailPt{Lat: d.Lat, Lon: d.Lon, AgeSec: now.Sub(d.At).Seconds()})
		}
		out = append(out, t)
	}
	s.mu.Lock()
	get := s.aprsGet
	extra := s.extraTgt
	s.mu.Unlock()
	if extra != nil {
		out = append(out, extra()...)
	}
	if get != nil {
		for _, st := range get() {
			out = append(out, Target{Kind: "aprs", ID: st.Call, Call: st.Call, Lat: st.Lat, Lon: st.Lon, HasPos: true, Speed: st.SpeedKt, Track: int(st.Course), AltFt: st.AltFt, AgeSec: st.AgeSec, Country: st.Country, Own: st.Own})
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleFlag(w http.ResponseWriter, r *http.Request) {
	cc := strings.TrimPrefix(r.URL.Path, "/api/flag/")
	data := ui.FlagPNG(strings.ToUpper(cc))
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=86400")
	w.Write(data)
}

// FT8MapEntry mirrors ui.MapEntry for the web map.
type FT8MapEntry struct {
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	FromLat float64 `json:"fromLat"`
	FromLon float64 `json:"fromLon"`
	Arc     bool    `json:"arc"`
	IsCQ    bool    `json:"cq"`
	Approx  bool    `json:"approx"`
	AgeSec  float64 `json:"ageSec"`
	Call    string  `json:"call"`
	ToCall  string  `json:"toCall"`
	Text    string  `json:"text"`
}

func (s *Server) handleFT8Map(w http.ResponseWriter, r *http.Request) {
	s.logMu.Lock()
	log := append([]FT8Line(nil), s.ft8Log...)
	s.logMu.Unlock()
	s.mu.Lock()
	get := s.ft8Grid
	s.mu.Unlock()
	out := []FT8MapEntry{}
	if get != nil {
		out = get(log)
	}
	writeJSON(w, out)
}

func (s *Server) handleAPRSLog(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	f := s.aprsLog
	s.mu.Unlock()
	if f == nil {
		writeJSON(w, APRSLog{})
		return
	}
	writeJSON(w, f())
}

// spec is one FFT frame of the full IF2 window (centred on the LO),
// downsampled to NBins dB values for the browser waterfall.
type spec struct {
	CentreHz int64   `json:"centreHz"`
	SpanHz   float64 `json:"spanHz"`
	Bins     []int16 `json:"bins"` // dB*10, clamped
	ListenHz int64   `json:"listenHz"`
	BwHz     float64 `json:"bwHz"`
	Wefax    bool    `json:"wefax"` // draw the fax tuning guides
	PbLo     float64 `json:"pbLo"`  // live passband edges vs listenHz
	PbHi     float64 `json:"pbHi"`  // (CW beat window, SSB band, FM ±bw/2)
	Mode     string  `json:"mode"`
	SigDb    float64 `json:"sigDb"`   // smoothed IF power, dBFS (S-meter)
	SqlOpen  bool    `json:"sqlOpen"` // squelch gate state (NFM)
	Reboot   bool    `json:"reboot"`
}

// Full-rate FFT on the raw tap with 8192 sent bins over the ENTIRE
// capture span: 250 Hz/bin at 2.048 Msps (31 Hz at 256 ksps), so
// ×128 zoom still resolves real detail. The FFT size adapts to the
// capture rate (largest power of two within ~130 ms of signal) so a
// waterfall row never smears more than ~130 ms.
const specBins = 8192
const specMaxFFT = 65536

// handleWefaxImg serves the current fax image downscaled (grayscale
// PNG, newest rows at the bottom). 204 while nothing is received.
// handleSSTVImg serves the in-progress or last SSTV raster as PNG.
func (s *Server) handleSSTVImg(w http.ResponseWriter, r *http.Request) {
	radio := s.radio
	if radio == nil || !radio.SSTVEnabled() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	img, name, line, total, _ := radio.SSTV().Snapshot(-1)
	if img == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-SSTV-Status", fmt.Sprintf("%s %d/%d", name, line, total))
	if err := png.Encode(w, img); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) handleWefaxImg(w http.ResponseWriter, r *http.Request) {
	if !s.radio.WefaxEnabled() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	prev, _, _ := s.radio.Wefax().Preview(400, 300)
	if prev == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	if err := png.Encode(w, prev); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) handleSpec(w http.ResponseWriter, r *http.Request) {
	// Full-rate tap: the web view shows the ENTIRE capture span (like
	// the device's wide waterfall), not just the IF2 slice.
	tap := s.radio.RawTap()
	if tap == nil {
		http.Error(w, "no tap", http.StatusServiceUnavailable)
		return
	}
	wefaxOn := s.radio.WefaxEnabled()
	pbLo, pbHi := s.radio.PassbandHz()
	s.specMu.Lock()
	defer s.specMu.Unlock()
	n := specMaxFFT
	if max := dsp.IQRate / 8000; max > 0 && max < n {
		n = 1 << (bits.Len(uint(max)) - 1) // largest power of two ≤ max
	}
	if n < 16384 || n > specMaxFFT {
		n = 16384
	}
	if len(s.specBuf) < specMaxFFT {
		s.specBuf = make([]complex128, specMaxFFT)
		s.specRe = make([]float64, specMaxFFT)
		s.specIm = make([]float64, specMaxFFT)
		s.specPow = make([]float64, specMaxFFT)
		s.specSort = make([]float64, specMaxFFT/4)
	}
	buf := s.specBuf[:n]
	gen := tap.SnapshotN(buf)
	if gen == 0 {
		writeJSON(w, spec{})
		return
	}
	re, im := s.specRe[:n], s.specIm[:n]
	for i, z := range buf {
		re[i], im[i] = real(z), imag(z)
	}
	dsp.FFT(re, im)
	// dB per bin (fftshifted so index 0 = lowest frequency) — the same
	// amplitude metric the device waterfall uses (20·log10|X|).
	pow := s.specPow[:n]
	for i := 0; i < n; i++ {
		k := (i + n/2) % n
		pow[i] = 20 * math.Log10(math.Hypot(re[k], im[k])+1e-12)
	}
	// Device-style mapping: dB relative to a tracked noise floor (25th
	// percentile, EMA 0.05 — same recipe as ui.NewSpectrumRow), so the
	// palette matches the handheld waterfall exactly. Bin value is
	// t*620 (0..620). The floor MUST stay in dB: a linear-power floor
	// runs into the hundreds/thousands after FFT amplification and
	// swamps the dB subtraction — that blacked the whole display out.
	// The percentile uses a stride-4 subsample: at 64k bins the
	// estimate barely moves and the sort gets 4× cheaper.
	j := 0
	for i := 0; i < n; i += 4 {
		s.specSort[j] = pow[i]
		j++
	}
	sub := s.specSort[:j]
	slices.Sort(sub)
	noise := sub[len(sub)/4]
	s.noise += 0.05 * (noise - s.noise)
	wfMinDb, wfMaxDb := 6.0, 62.0 // defaults until main wires the hooks
	s.mu.Lock()
	if s.wfGet != nil {
		wfMinDb, wfMaxDb = s.wfGet()
	}
	s.mu.Unlock()
	if wfMaxDb < wfMinDb+4 {
		wfMaxDb = wfMinDb + 4 // keep the colour range usable
	}
	bins := make([]int16, specBins)
	per := n / specBins
	for b := 0; b < specBins; b++ {
		mx := math.Inf(-1)
		for k := 0; k < per; k++ {
			if pow[b*per+k] > mx {
				mx = pow[b*per+k]
			}
		}
		db := mx - s.noise - wfMinDb
		t := db / wfMaxDb
		if t < 0 {
			t = 0
		}
		if t > 1 {
			t = 1
		}
		bins[b] = int16(t * 620)
	}
	sigSnap := s.radio.Snapshot()
	writeJSON(w, spec{
		CentreHz: s.radio.LO(),
		SpanHz:   float64(dsp.IQRate),
		Bins:     bins,
		ListenHz: s.radio.Freq(),
		BwHz:     s.radio.Bandwidth(),
		Wefax:    wefaxOn,
		PbLo:     pbLo,
		PbHi:     pbHi,
		Mode:     s.radio.Mode().Name,
		SigDb:    sigSnap.PowerDb,
		SqlOpen:  sigSnap.SquelchOpen,
		Reboot:   s.rebooting(),
	})
}

type layerInfo struct {
	Name string `json:"name"`
	Attr string `json:"attr"`
}

func (s *Server) handleLayers(w http.ResponseWriter, r *http.Request) {
	var out []layerInfo
	for _, l := range osm.Layers {
		if l.NoFetch {
			continue
		}
		out = append(out, layerInfo{Name: l.Name, Attr: l.Attr})
	}
	writeJSON(w, out)
}

// handleTile proxies one map tile through the handheld's own cache:
// memory → disk → upstream. With no internet the radar's disk cache
// still serves everything it has seen.
func (s *Server) handleTile(w http.ResponseWriter, r *http.Request) {
	var layer, z, x, y int
	if _, err := fmt.Sscanf(r.URL.Path, "/tiles/%d/%d/%d/%d", &layer, &z, &x, &y); err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if layer < 0 || layer >= len(osm.Layers) || osm.Layers[layer].NoFetch {
		http.NotFound(w, r)
		return
	}
	if z < 0 || z > 19 || x < 0 || y < 0 || x >= 1<<z || y >= 1<<z {
		http.NotFound(w, r)
		return
	}
	b, ct, err := s.tiles.TileBytes(layer, z, x, y)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(b)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}
