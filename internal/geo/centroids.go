// Package geo: country centroids for coarse station positions on the
// world map — used when a station's Maidenhead grid is not (yet) known.
// Coordinates are rough geographic centres, good enough to place a
// marker on the right country until the real grid arrives.
package geo

// countryCenter maps the DXCC country name (as used in dxcc.go) to its
// approximate centre {lat, lon}.
var countryCenter = map[string][2]float64{
	"Japan": {36, 138}, "China": {35, 103}, "Taiwan": {23.7, 121},
	"Thailand": {15, 101}, "Indonesia": {-2, 118}, "Singapore": {1.35, 103.8},
	"W.Malaysia": {4, 102}, "E.Malaysia": {3, 113}, "Philippines": {13, 122},
	"Cambodia": {12.5, 105}, "Laos": {18, 103}, "Myanmar": {20, 96},
	"Vietnam": {16, 106}, "Hong Kong": {22.3, 114.2},
	"USA": {39.8, -98.6}, "Puerto Rico": {18.2, -66.4}, "Virgin Is": {18.3, -64.9},
	"Hawaii": {20.3, -157}, "Canada": {54, -100},
	"UK": {52.5, -1.5}, "N.Ireland": {54.6, -6.7}, "Wales": {52.3, -3.7},
	"Scotland": {56.8, -4.2}, "IoM": {54.2, -4.5}, "Jersey": {49.2, -2.1},
	"Guernsey": {49.4, -2.6}, "Ireland": {53.3, -8},
	"France": {46.6, 2.4}, "Corsica": {42, 9},
	"Germany": {51, 10.4}, "Italy": {42.8, 12.5},
	"Spain": {40.2, -3.7}, "Canary Is": {28.2, -15.6}, "Portugal": {39.6, -8},
	"Belgium": {50.6, 4.6}, "Netherlands": {52.2, 5.4},
	"Denmark": {56, 9.7}, "Sweden": {61, 15}, "Norway": {62, 9},
	"Finland": {63.5, 26},
	"Czech Rep": {49.8, 15.3}, "Slovakia": {48.7, 19.5}, "Poland": {52, 19.4},
	"Romania": {45.9, 25}, "Hungary": {47.2, 19.4},
	"Latvia": {56.9, 24.6}, "Lithuania": {55.3, 23.9}, "Estonia": {58.7, 25.5},
	"Russia": {58, 60}, "Ukraine": {49, 31.4},
	"Slovenia": {46.1, 14.8}, "Croatia": {45.1, 15.2}, "Bosnia": {44, 17.8},
	"Macedonia": {41.6, 21.7}, "Albania": {41.1, 20.1},
	"Greece": {39.3, 22.5}, "Malta": {35.9, 14.4},
	"Israel": {31.4, 34.9}, "Turkey": {39, 35.3},
	"Kazakhstan": {48, 68}, "Uzbekistan": {41.4, 64.2}, "Tajikistan": {38.9, 71},
	"Armenia": {40.3, 45}, "Azerbaijan": {40.3, 47.7}, "Georgia": {42, 43.5},
	"Sri Lanka": {7.6, 80.7}, "Oman": {21, 57}, "UAE": {24, 54.3},
	"Qatar": {25.3, 51.2}, "Bahrain": {26, 50.5}, "Saudi Arabia": {24, 45},
	"Syria": {35, 38.5}, "Lebanon": {33.9, 35.9}, "Jordan": {31.3, 36.8},
	"India": {22.9, 79}, "Nepal": {28.3, 84}, "Bangladesh": {23.8, 90.2},
	"Pakistan": {30, 69.5},
	"Australia": {-25, 134}, "New Zealand": {-41.8, 172.8},
	"Brazil": {-10, -52}, "Argentina": {-35, -65}, "Chile": {-35, -71},
	"Peru": {-9.2, -75}, "Colombia": {4.1, -73}, "Venezuela": {7.1, -66.5},
	"Uruguay": {-32.8, -56}, "Paraguay": {-23.2, -58.4}, "Bolivia": {-16.7, -64.7},
	"Ecuador": {-1.4, -78.4}, "Costa Rica": {9.9, -84.2}, "Guatemala": {15.2, -90.3},
	"El Salvador": {13.8, -88.9}, "Panama": {8.6, -80.1}, "Jamaica": {18.1, -77.3},
	"Cuba": {21.6, -79.5}, "Dom. Rep": {18.9, -70.5},
	"Morocco": {32, -6.5}, "Algeria": {28, 2.6}, "Tunisia": {34, 9.5},
	"Egypt": {26.7, 30}, "Libya": {27, 17.3},
	"Tanzania": {-6.4, 34.8}, "Kenya": {0.3, 37.9},
	"South Africa": {-29, 25}, "Namibia": {-22, 17.2}, "Mozambique": {-17.3, 35.5},
	"Gabon": {-0.6, 11.8}, "Cameroon": {5.7, 12.7}, "Ghana": {7.9, -1},
	"Senegal": {14.4, -14.5}, "Zambia": {-13.5, 27.8}, "Botswana": {-22.3, 24.7},
	"Uganda": {1.3, 32.4},
	"Andorra": {42.5, 1.5}, "San Marino": {43.9, 12.5}, "Monaco": {43.7, 7.4},
	"Switzerland": {46.8, 8.2},
}

// CountryLatLon resolves a callsign to its country's approximate centre
// via the DXCC prefix table. ok is false for unknown prefixes.
func CountryLatLon(call string) (lat, lon float64, ok bool) {
	c := Country(call)
	if c == "" {
		return 0, 0, false
	}
	ll, found := countryCenter[c]
	if !found {
		return 0, 0, false
	}
	return ll[0], ll[1], true
}
