package geo

// nameISO maps the DXCC country names used by dxcc.go to ISO 3166-1
// alpha-2 codes (for flag icons). Sub-national entries map to the
// sovereign state's flag.
var nameISO = map[string]string{
	"Albania": "AL", "Algeria": "DZ", "Andorra": "AD", "Argentina": "AR",
	"Armenia": "AM", "Australia": "AU", "Azerbaijan": "AZ", "Bahrain": "BH",
	"Bangladesh": "BD", "Belgium": "BE", "Bolivia": "BO", "Bosnia": "BA",
	"Botswana": "BW", "Brazil": "BR", "Cambodia": "KH", "Cameroon": "CM",
	"Canada": "CA", "Canary Is": "ES", "Chile": "CL", "China": "CN",
	"Colombia": "CO", "Corsica": "FR", "Costa Rica": "CR", "Croatia": "HR",
	"Cuba": "CU", "Czech Rep": "CZ", "Denmark": "DK", "Dom. Rep": "DO",
	"E.Malaysia": "MY", "Ecuador": "EC", "Egypt": "EG", "El Salvador": "SV",
	"Estonia": "EE", "Finland": "FI", "France": "FR", "Gabon": "GA",
	"Georgia": "GE", "Germany": "DE", "Ghana": "GH", "Greece": "GR",
	"Guatemala": "GT", "Guernsey": "GG", "Hawaii": "US", "Hong Kong": "HK",
	"Hungary": "HU", "India": "IN", "Indonesia": "ID", "IoM": "IM",
	"Ireland": "IE", "Israel": "IL", "Italy": "IT", "Jamaica": "JM",
	"Japan": "JP", "Jersey": "JE", "Jordan": "JO", "Kazakhstan": "KZ",
	"Kenya": "KE", "Laos": "LA", "Latvia": "LV", "Lebanon": "LB",
	"Libya": "LY", "Lithuania": "LT", "Macedonia": "MK", "Malta": "MT",
	"Monaco": "MC", "Morocco": "MA", "Mozambique": "MZ", "Myanmar": "MM",
	"N.Ireland": "GB", "Namibia": "NA", "Nepal": "NP", "Netherlands": "NL",
	"New Zealand": "NZ", "Norway": "NO", "Oman": "OM", "Pakistan": "PK",
	"Panama": "PA", "Paraguay": "PY", "Peru": "PE", "Philippines": "PH",
	"Poland": "PL", "Portugal": "PT", "Puerto Rico": "PR", "Qatar": "QA",
	"Romania": "RO", "Russia": "RU", "San Marino": "SM", "Saudi Arabia": "SA",
	"Scotland": "GB", "Senegal": "SN", "Singapore": "SG", "Slovakia": "SK",
	"Slovenia": "SI", "South Africa": "ZA", "Spain": "ES", "Sri Lanka": "LK",
	"Sweden": "SE", "Switzerland": "CH", "Syria": "SY", "Taiwan": "TW",
	"Tajikistan": "TJ", "Tanzania": "TZ", "Thailand": "TH", "Tunisia": "TN",
	"Turkey": "TR", "Uganda": "UG", "Ukraine": "UA", "UAE": "AE",
	"UK": "GB", "Uruguay": "UY", "USA": "US", "Uzbekistan": "UZ",
	"Venezuela": "VE", "Vietnam": "VN", "Virgin Is": "VI",
	"W.Malaysia": "MY", "Wales": "GB", "Zambia": "ZM",
}

// CountryISO returns the ISO 3166-1 alpha-2 code for a callsign's DXCC
// country, or "" if unknown.
func CountryISO(call string) string {
	return nameISO[Country(call)]
}
