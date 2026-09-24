package geo

import (
	"sort"
	"strings"
)

// dxcc maps callsign prefixes to country names.
var dxcc = map[string]string{
	"JA": "Japan", "JE": "Japan", "JF": "Japan", "JG": "Japan", "JH": "Japan",
	"JI": "Japan", "JJ": "Japan", "JK": "Japan", "JL": "Japan", "JM": "Japan",
	"JN": "Japan", "JO": "Japan", "JP": "Japan", "JQ": "Japan", "JR": "Japan", "JS": "Japan",
	"7J": "Japan", "7K": "Japan", "7L": "Japan", "7M": "Japan", "7N": "Japan",
	"BG": "China", "BH": "China", "BI": "China", "BJ": "China", "BT": "China", "BY": "China",
	"B": "China",
	"BM": "Taiwan", "BN": "Taiwan", "BO": "Taiwan", "BU": "Taiwan", "BV": "Taiwan", "BW": "Taiwan",
	"HS": "Thailand", "E2": "Thailand",
	"YB": "Indonesia", "YC": "Indonesia", "YD": "Indonesia", "YE": "Indonesia",
	"YF": "Indonesia", "YG": "Indonesia", "YH": "Indonesia",
	"7A": "Indonesia", "7B": "Indonesia", "7C": "Indonesia", "7I": "Indonesia", "8A": "Indonesia",
	"9V": "Singapore", "9M2": "W.Malaysia", "9M4": "E.Malaysia", "9W2": "W.Malaysia", "9W4": "E.Malaysia",
	"DU": "Philippines", "DV": "Philippines", "DW": "Philippines", "DX": "Philippines",
	"DY": "Philippines", "DZ": "Philippines",
	"4D": "Philippines", "4E": "Philippines", "4F": "Philippines", "4G": "Philippines", "4I": "Philippines",
	"XU": "Cambodia", "XW": "Laos", "XY": "Myanmar", "XZ": "Myanmar", "3W": "Vietnam",
	"VR2": "Hong Kong", "VR": "Hong Kong",
	"W": "USA", "K": "USA", "N": "USA", "AA": "USA", "AB": "USA", "AC": "USA", "AD": "USA",
	"AE": "USA", "AF": "USA", "AG": "USA", "AI": "USA", "AJ": "USA", "AK": "USA", "AL": "USA",
	"KP4": "Puerto Rico", "NP4": "Puerto Rico", "WP4": "Puerto Rico", "KP2": "Virgin Is",
	"KH6": "Hawaii", "NH6": "Hawaii", "WH6": "Hawaii",
	"VE": "Canada", "VA": "Canada", "VO": "Canada", "VY": "Canada", "CY": "Canada", "CZ": "Canada",
	"G": "UK", "M": "UK", "2E": "UK",
	"GI": "N.Ireland", "GW": "Wales", "GM": "Scotland", "GD": "IoM", "GJ": "Jersey", "GU": "Guernsey",
	"EI": "Ireland", "F": "France", "TK": "Corsica",
	"DL": "Germany", "DA": "Germany", "DB": "Germany", "DC": "Germany", "DD": "Germany",
	"DE": "Germany", "DF": "Germany", "DG": "Germany", "DH": "Germany", "DJ": "Germany",
	"DK": "Germany", "DM": "Germany", "DN": "Germany", "DO": "Germany",
	"I": "Italy", "IZ": "Italy", "IK": "Italy", "IU": "Italy", "IW": "Italy",
	"EA": "Spain", "EB": "Spain", "EC": "Spain", "ED": "Spain", "EE": "Spain",
	"EF": "Spain", "EG": "Spain", "EH": "Spain", "EA8": "Canary Is",
	"CT": "Portugal", "CQ": "Portugal", "CR": "Portugal",
	"ON": "Belgium", "OO": "Belgium", "OP": "Belgium", "OQ": "Belgium",
	"PA": "Netherlands", "PB": "Netherlands", "PC": "Netherlands", "PD": "Netherlands",
	"PE": "Netherlands", "PF": "Netherlands", "PG": "Netherlands", "PH": "Netherlands", "PI": "Netherlands",
	"OZ": "Denmark", "OU": "Denmark", "OV": "Denmark", "OW": "Denmark", "5P": "Denmark",
	"SM": "Sweden", "SA": "Sweden", "SB": "Sweden", "SC": "Sweden", "SD": "Sweden",
	"SE": "Sweden", "SF": "Sweden", "SG": "Sweden", "SH": "Sweden", "SI": "Sweden",
	"SJ": "Sweden", "SK": "Sweden", "SL": "Sweden",
	"LA": "Norway", "LB": "Norway", "LC": "Norway", "LD": "Norway", "LE": "Norway",
	"LF": "Norway", "LG": "Norway", "LH": "Norway", "LI": "Norway", "LJ": "Norway",
	"LK": "Norway", "LL": "Norway", "LM": "Norway", "LN": "Norway",
	"OH": "Finland", "OF": "Finland", "OG": "Finland", "OI": "Finland",
	"OK": "Czech Rep", "OL": "Czech Rep",
	"OM": "Slovakia", "SN": "Slovakia", "SO": "Slovakia",
	"SP": "Poland", "SQ": "Poland", "SR": "Poland", "3Z": "Poland", "HF": "Poland",
	"YO": "Romania", "YP": "Romania", "YQ": "Romania", "YR": "Romania",
	"HA": "Hungary", "HG": "Hungary",
	"YL": "Latvia", "LY": "Lithuania", "ES": "Estonia",
	"UA": "Russia", "UB": "Russia", "UC": "Russia", "UD": "Russia", "UE": "Russia",
	"UF": "Russia", "UG": "Russia", "UH": "Russia", "UI": "Russia",
	"RA": "Russia", "R": "Russia",
	"UX": "Ukraine", "UR": "Ukraine", "US": "Ukraine", "UT": "Ukraine",
	"UU": "Ukraine", "UV": "Ukraine", "UW": "Ukraine", "EM": "Ukraine", "EN": "Ukraine",
	"S5": "Slovenia", "OT": "Slovenia", "9A": "Croatia", "E7": "Bosnia",
	"Z3": "Macedonia", "ZA": "Albania",
	"SV": "Greece", "SW": "Greece", "SX": "Greece", "SY": "Greece", "SZ": "Greece",
	"9H": "Malta",
	"4X": "Israel", "4Z": "Israel",
	"TA": "Turkey", "TB": "Turkey", "TC": "Turkey",
	"UN": "Kazakhstan", "UP": "Kazakhstan", "UQ": "Kazakhstan",
	"EX": "Uzbekistan", "EZ": "Tajikistan", "EK": "Armenia",
	"4J": "Azerbaijan", "4L": "Azerbaijan", "4K": "Georgia",
	"4S": "Sri Lanka", "A4": "Oman", "A6": "UAE", "A7": "Qatar", "A9": "Bahrain",
	"HZ": "Saudi Arabia", "7Z": "Saudi Arabia",
	"YK": "Syria", "OD": "Lebanon", "JY": "Jordan",
	"VU": "India", "9N": "Nepal", "S2": "Bangladesh", "AP": "Pakistan",
	"VK": "Australia", "ZL": "New Zealand", "ZM": "New Zealand",
	"PY": "Brazil", "PP": "Brazil", "PQ": "Brazil", "PR": "Brazil", "PS": "Brazil",
	"PT": "Brazil", "PU": "Brazil", "ZV": "Brazil", "ZW": "Brazil", "ZX": "Brazil",
	"LU": "Argentina", "LO": "Argentina", "LP": "Argentina", "LQ": "Argentina",
	"LR": "Argentina", "LS": "Argentina", "LT": "Argentina", "LV": "Argentina",
	"LW": "Argentina", "AY": "Argentina",
	"CE": "Chile", "CA": "Chile", "CB": "Chile", "CC": "Chile", "CD": "Chile",
	"OA": "Peru", "OB": "Peru", "OC": "Peru",
	"HK": "Colombia", "HJ": "Colombia", "5J": "Colombia",
	"YV": "Venezuela", "YW": "Venezuela", "YX": "Venezuela", "YY": "Venezuela",
	"CX": "Uruguay", "ZP": "Paraguay", "CP": "Bolivia", "HC": "Ecuador",
	"TI": "Costa Rica", "TG": "Guatemala", "YS": "El Salvador", "HP": "Panama",
	"6Y": "Jamaica", "CO": "Cuba", "CM": "Cuba", "CL": "Cuba", "HI": "Dom. Rep",
	"CN": "Morocco", "7X": "Algeria", "3V": "Tunisia", "SU": "Egypt",
	"5A": "Libya", "5H": "Tanzania", "5Z": "Kenya",
	"ZS": "South Africa", "V5": "Namibia", "C9": "Mozambique",
	"TR": "Gabon", "TJ": "Cameroon", "9G": "Ghana", "6W": "Senegal",
	"9J": "Zambia", "A2": "Botswana", "5X": "Uganda",
	"C3": "Andorra", "T7": "San Marino", "3A": "Monaco", "HB": "Switzerland",
}

// sortedPrefixes caches keys sorted longest-first for greedy matching.
var sortedPrefixes = func() []string {
	keys := make([]string, 0, len(dxcc))
	for k := range dxcc {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}()

// Country looks up a callsign's DXCC country by its prefix.
func Country(call string) string {
	c := strings.ToUpper(strings.TrimSpace(call))
	for _, p := range sortedPrefixes {
		if strings.HasPrefix(c, p) {
			return dxcc[p]
		}
	}
	return ""
}
