package aprs

import (
	"math"
	"sort"
	"sync"
	"time"
)

// StationTTL is how long a decoded station stays on the radar.
const StationTTL = 45 * time.Minute

// Station is one decoded APRS station.
type Station struct {
	Call      string
	Lat, Lon  float64
	Table     byte
	Sym       byte
	CourseDeg float64
	SpeedKt   float64
	HasCS     bool
	AltFt     int
	HasAlt    bool
	Comment   string
	LastHeard time.Time
}

// Store keeps the newest report per callsign.
type Store struct {
	mu sync.Mutex
	m  map[string]*Station
}

func NewStore() *Store { return &Store{m: map[string]*Station{}} }

// ProcessFrame decodes one validated frame body and, when it carries a
// position, records the station. Returns the station so callers can
// toast "heard XXXX".
func (s *Store) ProcessFrame(body []byte) *Station {
	f := DecodeFrame(body)
	if f == nil || len(f.Info) == 0 {
		return nil
	}
	if f.Control != 0x03 || f.PID != 0xF0 {
		return nil // not an APRS UI frame
	}
	p, ok := ParsePosition(f.Info)
	if !ok {
		return nil
	}
	st := &Station{
		Call: f.Src, Lat: p.Lat, Lon: p.Lon, Table: p.Table, Sym: p.Sym,
		CourseDeg: p.CourseDeg, SpeedKt: p.SpeedKt, HasCS: p.HasCS,
		AltFt: p.AltFt, HasAlt: p.HasAlt, Comment: p.Comment,
		LastHeard: time.Now(),
	}
	s.mu.Lock()
	s.m[st.Call] = st
	s.mu.Unlock()
	return st
}

// All returns stations heard inside the TTL, newest first.
func (s *Store) All() []Station {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Station, 0, len(s.m))
	cut := time.Now().Add(-StationTTL)
	for _, st := range s.m {
		if st.LastHeard.Before(cut) {
			continue
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHeard.After(out[j].LastHeard) })
	return out
}

// Count is the number of live stations (for the menu row).
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	cut := time.Now().Add(-StationTTL)
	for _, st := range s.m {
		if !st.LastHeard.Before(cut) {
			n++
		}
	}
	return n
}

// BeaconTimer returns the next beacon interval for the SmartBeaconing
// algorithm given the current speed in knots (0 when stationary).
func BeaconInterval(speedKt float64, fastSec, slowSec int) int {
	const lowKt, highKt = 3, 50
	if speedKt <= lowKt {
		return slowSec
	}
	if speedKt >= highKt {
		return fastSec
	}
	t := (speedKt - lowKt) / (highKt - lowKt)
	return int(float64(slowSec) - t*float64(slowSec-fastSec))
}

// TurnBeaconDue decides a corner-pegging beacon: heading change over
// threshold and enough time since the last beacon.
func TurnBeaconDue(courseDeg float64, speedKt float64, lastCourseDeg float64, sinceLast time.Duration, minTurnSec int) bool {
	if speedKt < 3 {
		return false
	}
	d := courseDeg - lastCourseDeg
	for d > 180 {
		d -= 360
	}
	for d < -180 {
		d += 360
	}
	threshold := 15.0 + 25.0*3/math.Max(speedKt, 1) // slope: faster = smaller
	if d < 0 {
		d = -d
	}
	return d > threshold && sinceLast > time.Duration(minTurnSec)*time.Second
}
