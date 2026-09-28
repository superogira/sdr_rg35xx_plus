// ADS-B (DF17/DF18 extended squitter) decoding: airborne position via
// Compact Position Reporting, identity, and velocity. CRC is not
// re-verified — Beast feeds (dump1090/readsb) only emit frames that
// passed their CRC check.
package adsb

import (
	"math"
	"sync"
	"time"
)

// cprBoundaries: latitudes below which the zone count NL takes each
// value from 59 down to 2 (ICAO Annex 10 CPR tables).
var cprBoundaries = []float64{
	10.4704712, 14.8281744, 18.1862636, 21.0293949, 23.5450492,
	25.8292471, 27.9389871, 29.9113569, 31.7720975, 33.5399654,
	35.2289960, 36.8502511, 38.4124189, 39.9225668, 41.3865183,
	42.8081401, 44.1915498, 45.5402911, 46.8572311, 48.1449355,
	49.4055748, 50.6413768, 51.8546525, 53.0472965, 54.2210519,
	55.3773540, 56.5175373, 57.6426895, 58.7537206, 59.8517327,
	60.9377148, 62.0125132, 63.0767427, 64.1308863, 65.1755040,
	66.2109305, 67.2374953, 68.2555861, 69.2655688, 70.2678222,
	71.2625572, 72.2500269, 73.2304877, 74.2040941, 75.1709847,
	76.1315119, 77.0858914, 78.0343104, 78.9769309, 79.9140611,
	80.8458489, 81.7724789, 82.6939714, 83.6104640, 84.5220350,
	85.4296973, 86.3325706, 87.2307885, 88.1244215, 89.0135132,
	89.8982529,
}

// cprNL returns the number of longitude zones at a latitude.
func cprNL(lat float64) int {
	a := math.Abs(lat)
	for i, b := range cprBoundaries {
		if a < b {
			return 59 - i
		}
	}
	return 1
}

// cprPos is one CPR position report (odd or even).
type cprPos struct {
	latCpr, lonCpr float64 // normalised 0..1
	at             time.Time
}

// TrailPt is one breadcrumb of the aircraft's flown path.
type TrailPt struct {
	Lat, Lon float64
	AltFt    int
	At       time.Time
}

// Plane is one tracked aircraft.
type Plane struct {
	ICAO     string
	Callsign string
	Lat, Lon float64
	HasPos   bool
	AltFt    int
	SpeedKt  int
	TrackDeg int
	VrateFpm int
	LastSeen time.Time
	Trail    []TrailPt // last minute of flown positions

	even, odd *cprPos
}

// Store holds aircraft by ICAO, pruned by TTL.
type Store struct {
	mu     sync.Mutex
	planes map[string]*Plane
	ttl    time.Duration
}

func NewStore() *Store {
	return &Store{planes: map[string]*Plane{}, ttl: 60 * time.Second}
}

// Planes returns a snapshot of live aircraft (no positions yet, or
// with positions).
func (s *Store) Planes() []*Plane {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Plane
	for _, p := range s.planes {
		if time.Since(p.LastSeen) > s.ttl {
			continue
		}
		cp := *p
		if len(p.Trail) > 0 {
			cp.Trail = append([]TrailPt(nil), p.Trail...)
		}
		out = append(out, &cp)
	}
	return out
}

// CountLive is the number of aircraft seen within the TTL.
func (s *Store) CountLive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, p := range s.planes {
		if time.Since(p.LastSeen) <= s.ttl {
			n++
		}
	}
	return n
}

func (s *Store) plane(icao string) *Plane {
	p := s.planes[icao]
	if p == nil {
		p = &Plane{ICAO: icao}
		s.planes[icao] = p
	}
	return p
}

// prune drops stale entries (called opportunistically on decode).
func (s *Store) prune() {
	for icao, p := range s.planes {
		if time.Since(p.LastSeen) > 3*s.ttl {
			delete(s.planes, icao)
		}
	}
}

// Decode consumes one Mode S long (14-byte) or short (7-byte) message.
func (s *Store) Decode(msg []byte) {
	if len(msg) < 7 {
		return
	}
	df := msg[0] >> 3
	if df != 17 && df != 18 {
		return // not ADS-B
	}
	icao := hex3(msg[1], msg[2], msg[3])
	if len(msg) < 14 {
		return // short frames carry no extended squitter
	}
	me := msg[4:11]
	tc := me[0] >> 3
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	p := s.plane(icao)
	p.LastSeen = time.Now()
	switch {
	case tc >= 1 && tc <= 4:
		p.Callsign = decodeCallsign(me)
	case tc >= 9 && tc <= 18:
		s.decodeAirbornePos(p, me)
	case tc == 19:
		decodeVelocity(p, me)
	}
	// Surface positions (TC 5-8) are not decoded in v1 — aircraft
	// taxiing near the receiver show callsign only.
}

// decodeAirbornePos handles TC 9-18: altitude plus CPR latitude and
// longitude; a global decode runs once both odd and even frames exist.
func (s *Store) decodeAirbornePos(p *Plane, me []byte) {
	// Altitude: 12 bits at ME[1..2], 25 ft quantinity when Q set.
	ac := (int(me[1])<<4 | int(me[2])>>4) & 0xFFF
	q := (ac >> 4) & 1
	if q == 1 {
		p.AltFt = ((ac&0xF)|((ac&0xFE0)>>1))*25 - 1000
	}

	odd := (me[2]>>2)&1 == 1
	pos := cprPos{
		latCpr: float64(int(me[2]&3)<<15|int(me[3])<<7|int(me[4])>>1) / 131072.0,
		lonCpr: float64(int(me[4]&1)<<16|int(me[5])<<8|int(me[6])) / 131072.0,
		at:     time.Now(),
	}
	if odd {
		p.odd = &pos
	} else {
		p.even = &pos
	}
	s.globalDecode(p, odd)
}

// globalDecode computes lat/lon from the latest even+odd CPR pair.
// `latestOdd` picks which parity's latitude/longitude grid to use (the
// convention: the position corresponds to the MORE RECENT frame).
func (s *Store) globalDecode(p *Plane, latestOdd bool) {
	if p.even == nil || p.odd == nil {
		return
	}
	const nz = 15
	dlat0 := 360.0 / (4 * nz)
	dlat1 := 360.0 / (4*nz - 1)
	j := math.Floor(59*p.even.latCpr - 60*p.odd.latCpr + 0.5)
	rlat0 := dlat0 * (math.Mod(j, 60) + p.even.latCpr)
	rlat1 := dlat1 * (math.Mod(j, 59) + p.odd.latCpr)
	if rlat0 < -90 || rlat0 > 90 || rlat1 < -90 || rlat1 > 90 {
		return
	}
	if cprNL(rlat0) != cprNL(rlat1) {
		return
	}
	lat, i := rlat0, 0
	if latestOdd {
		lat, i = rlat1, 1
	}
	ni := cprNL(lat) - i
	if ni < 1 {
		ni = 1
	}
	dlon := 360.0 / float64(ni)
	m := math.Floor(p.even.lonCpr*float64(cprNL(lat)-1) - p.odd.lonCpr*float64(cprNL(lat)) + 0.5)
	lon := dlon * (math.Mod(m, float64(ni)) + map[bool]float64{false: p.even.lonCpr, true: p.odd.lonCpr}[latestOdd])
	for lon > 180 {
		lon -= 360
	}
	for lon < -180 {
		lon += 360
	}
	p.Lat, p.Lon, p.HasPos = lat, lon, true
	// Breadcrumbs: one dot every 5 s of flight within a five-minute
	// window (~60 dots). Position fixes arrive ~1/s — laying a dot on
	// every fix made a dense 1-px smear that was invisible on the
	// handheld screen; spaced dots are the classic readable form.
	now := time.Now()
	if len(p.Trail) == 0 || now.Sub(p.Trail[len(p.Trail)-1].At) >= 5*time.Second {
		p.Trail = append(p.Trail, TrailPt{Lat: lat, Lon: lon, AltFt: p.AltFt, At: now})
	}
	cut := now.Add(-5 * time.Minute)
	drop := 0
	for drop < len(p.Trail) && !p.Trail[drop].At.After(cut) {
		drop++
	}
	if drop > 0 {
		p.Trail = p.Trail[drop:]
	}
	if len(p.Trail) > 70 {
		p.Trail = p.Trail[len(p.Trail)-70:]
	}
}

// decodeCallsign reads BDS 0,8 (TC 1-4): 8 characters, 6 bits each.
func decodeCallsign(me []byte) string {
	six := [8]int{
		int(me[1]) >> 2,
		int(me[1]&3)<<4 | int(me[2])>>4,
		int(me[2]&15)<<2 | int(me[3])>>6,
		int(me[3]) & 63,
		int(me[4]) >> 2,
		int(me[4]&3)<<4 | int(me[5])>>4,
		int(me[5]&15)<<2 | int(me[6])>>6,
		int(me[6]) & 63,
	}
	var out []byte
	for _, v := range six {
		switch {
		case v >= 1 && v <= 26:
			out = append(out, byte('A'+v-1))
		case v == 32:
			out = append(out, ' ')
		case v >= 48 && v <= 57:
			out = append(out, byte(v-48+'0'))
		default:
			out = append(out, '_')
		}
	}
	// Trim trailing padding.
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}

// decodeVelocity handles TC 19 subtype 1 (ground speed + track).
func decodeVelocity(p *Plane, me []byte) {
	sub := me[0] & 7
	if sub != 1 && sub != 2 {
		return // supersonic / TAS variants not needed for the radar
	}
	if sub == 2 {
		return // IAS/TAS heading form — skip for v1
	}
	ewSign := 1
	if me[1]&0x04 != 0 {
		ewSign = -1
	}
	ew := ewSign * (int(me[1]&3)<<8 | int(me[2]))
	nsSign := 1
	if me[3]&0x80 != 0 {
		nsSign = -1
	}
	ns := nsSign * (int(me[3]&0x7F)<<3 | int(me[4])>>5)
	if ew == 0 && ns == 0 {
		return
	}
	p.SpeedKt = int(math.Round(math.Sqrt(float64(ew*ew + ns*ns))))
	track := math.Atan2(float64(ew), float64(ns)) * 180 / math.Pi
	if track < 0 {
		track += 360
	}
	p.TrackDeg = int(track) % 360

	// Vertical rate: bits 36-45, sign bit 35, in 64 fpm steps.
	vrSign := 1
	if me[4]&0x08 != 0 {
		vrSign = -1
	}
	vr := vrSign * ((int(me[4]&7)<<6 | int(me[5])>>2) * 64)
	p.VrateFpm = vr
}

func hex3(a, b, c byte) string {
	const hex = "0123456789ABCDEF"
	return string([]byte{hex[a>>4], hex[a&15], hex[b>>4], hex[b&15], hex[c>>4], hex[c&15]})
}
