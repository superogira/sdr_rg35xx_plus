// Package geo: Maidenhead grid math (lat/lon, great-circle distance)
// and callsign-prefix → country lookup for FT8 annotations.
package geo

import (
	"math"
	"strings"
)

// GridToLatLon converts a 4- or 6-character Maidenhead locator to its
// centre coordinates.
func GridToLatLon(grid string) (lat, lon float64, ok bool) {
	g := strings.ToUpper(strings.TrimSpace(grid))
	if len(g) < 4 {
		return 0, 0, false
	}
	f1 := g[0] - 'A'
	f2 := g[1] - 'A'
	s1 := g[2] - '0'
	s2 := g[3] - '0'
	if f1 > 17 || f2 > 17 || s1 > 9 || s2 > 9 {
		return 0, 0, false
	}
	lon = -180 + float64(f1)*20 + float64(s1)*2
	lat = -90 + float64(f2)*10 + float64(s2)
	if len(g) >= 6 {
		u1 := g[4] - 'A'
		u2 := g[5] - 'A'
		if u1 > 23 || u2 > 23 {
			return 0, 0, false
		}
		lon += float64(u1) * (2.0 / 24)
		lat += float64(u2) * (1.0 / 24)
	}
	// Centre of the cell.
	lon += 1
	lat += 0.5
	if len(g) >= 6 {
		lon += 2.0 / 48
		lat += 1.0 / 48
	}
	return lat, lon, true
}

// DistanceKm returns the great-circle distance between two grids.
func DistanceKm(grid1, grid2 string) int {
	lat1, lon1, ok1 := GridToLatLon(grid1)
	lat2, lon2, ok2 := GridToLatLon(grid2)
	if !ok1 || !ok2 {
		return -1
	}
	rLat1, rLon1 := lat1*math.Pi/180, lon1*math.Pi/180
	rLat2, rLon2 := lat2*math.Pi/180, lon2*math.Pi/180
	dLat := rLat2 - rLat1
	dLon := rLon2 - rLon1
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rLat1)*math.Cos(rLat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return int(math.Round(6371 * 2 * math.Asin(math.Sqrt(a))))
}
