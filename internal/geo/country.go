package geo

import "strings"

// ICAOCountry returns the ISO 3166-1 alpha-2 country code from an ICAO
// 24-bit address (hex string). Returns "" if unknown.
func ICAOCountry(icao string) string {
	icao = strings.ToUpper(icao)
	if len(icao) != 6 {
		return ""
	}
	// ICAO allocation blocks (first 3 hex digits → country).
	// Incomplete but covers common ranges.
	switch {
	case icao >= "008000" && icao <= "00FFFF":
		return "ZA" // South Africa
	case icao >= "010000" && icao <= "017FFF":
		return "EG" // Egypt
	case icao >= "018000" && icao <= "01FFFF":
		return "LY" // Libya
	case icao >= "020000" && icao <= "027FFF":
		return "MA" // Morocco
	case icao >= "028000" && icao <= "02FFFF":
		return "TN" // Tunisia
	case icao >= "300000" && icao <= "33FFFF":
		return "ES" // Spain
	case icao >= "340000" && icao <= "37FFFF":
		return "FR" // France
	case icao >= "380000" && icao <= "3BFFFF":
		return "IT" // Italy
	case icao >= "3C0000" && icao <= "3FFFFF":
		return "DE" // Germany
	case icao >= "400000" && icao <= "43FFFF":
		return "GB" // United Kingdom
	case icao >= "440000" && icao <= "447FFF":
		return "AT" // Austria
	case icao >= "448000" && icao <= "44FFFF":
		return "BE" // Belgium
	case icao >= "4C0000" && icao <= "4CFFFF":
		return "IE" // Ireland
	case icao >= "450000" && icao <= "457FFF":
		return "DK" // Denmark
	case icao >= "458000" && icao <= "45FFFF":
		return "FI" // Finland
	case icao >= "460000" && icao <= "467FFF":
		return "NO" // Norway
	case icao >= "468000" && icao <= "46FFFF":
		return "SE" // Sweden
	case icao >= "470000" && icao <= "477FFF":
		return "CZ" // Czech Republic
	case icao >= "478000" && icao <= "47FFFF":
		return "GR" // Greece
	case icao >= "480000" && icao <= "487FFF":
		return "HU" // Hungary
	case icao >= "488000" && icao <= "48FFFF":
		return "PL" // Poland
	case icao >= "490000" && icao <= "497FFF":
		return "PT" // Portugal
	case icao >= "498000" && icao <= "49FFFF":
		return "RO" // Romania
	case icao >= "4A0000" && icao <= "4A7FFF":
		return "RU" // Russia
	case icao >= "500000" && icao <= "5003FF":
		return "BD" // Bangladesh
	case icao >= "500400" && icao <= "5007FF":
		return "MM" // Myanmar
	case icao >= "500800" && icao <= "500BFF":
		return "BT" // Bhutan
	case icao >= "500C00" && icao <= "500FFF":
		return "KH" // Cambodia
	case icao >= "501000" && icao <= "5013FF":
		return "LK" // Sri Lanka
	case icao >= "600000" && icao <= "6003FF":
		return "CY" // Cyprus
	case icao >= "680000" && icao <= "6BFFFF":
		return "SA" // Saudi Arabia
	case icao >= "700000" && icao <= "707FFF":
		return "AF" // Afghanistan
	case icao >= "710000" && icao <= "717FFF":
		return "KW" // Kuwait
	case icao >= "738000" && icao <= "73FFFF":
		return "IQ" // Iraq
	case icao >= "740000" && icao <= "747FFF":
		return "IR" // Iran
	case icao >= "748000" && icao <= "74FFFF":
		return "IL" // Israel
	case icao >= "750000" && icao <= "757FFF":
		return "JO" // Jordan
	case icao >= "758000" && icao <= "75FFFF":
		return "LB" // Lebanon
	case icao >= "760000" && icao <= "767FFF":
		return "MY" // Malaysia
	case icao >= "800000" && icao <= "807FFF":
		return "IN" // India
	case icao >= "880000" && icao <= "887FFF":
		return "TH" // Thailand
	case icao >= "888000" && icao <= "88FFFF":
		return "KP" // North Korea
	case icao >= "890000" && icao <= "897FFF":
		return "KR" // South Korea
	case icao >= "8A0000" && icao <= "8A7FFF":
		return "PK" // Pakistan
	case icao >= "8A8000" && icao <= "8AFFFF":
		return "PH" // Philippines
	case icao >= "8B0000" && icao <= "8B7FFF":
		return "SG" // Singapore
	case icao >= "900000" && icao <= "97FFFF":
		return "CN" // China
	case icao >= "A00000" && icao <= "A7FFFF":
		return "US" // United States
	case icao >= "C00000" && icao <= "C3FFFF":
		return "CA" // Canada
	case icao >= "C80000" && icao <= "C8FFFF":
		return "NZ" // New Zealand
	case icao >= "C90000" && icao <= "C9FFFF":
		return "AU" // Australia
	case icao >= "E00000" && icao <= "E3FFFF":
		return "AR" // Argentina
	case icao >= "E40000" && icao <= "E7FFFF":
		return "BR" // Brazil
	case icao >= "E80000" && icao <= "E87FFF":
		return "CL" // Chile
	default:
		return ""
	}
}

// MMSICountry returns the ISO 3166-1 alpha-2 country code from a 9-digit
// MMSI (Maritime Mobile Service Identity). Returns "" if unknown.
func MMSICountry(mmsi string) string {
	if len(mmsi) != 9 {
		return ""
	}
	// MID (Maritime Identification Digits) = first 3 digits.
	mid := mmsi[:3]
	switch mid {
	case "201":
		return "AL" // Albania
	case "202":
		return "AD" // Andorra
	case "203":
		return "AT" // Austria
	case "204", "205", "206":
		return "PT" // Portugal (incl. Azores/Madeira)
	case "207":
		return "BE" // Belgium
	case "208":
		return "BY" // Belarus
	case "209", "210", "211":
		return "CY" // Cyprus
	case "212":
		return "CY" // Cyprus
	case "213":
		return "GE" // Georgia
	case "214", "215", "216":
		return "MD" // Moldova
	case "218":
		return "DE" // Germany
	case "219":
		return "DK" // Denmark
	case "220":
		return "DK" // Denmark
	case "224":
		return "ES" // Spain
	case "225":
		return "ES" // Spain
	case "226":
		return "FR" // France
	case "227":
		return "FR" // France
	case "228":
		return "FR" // France
	case "229":
		return "MT" // Malta
	case "230":
		return "FI" // Finland
	case "231":
		return "FO" // Faroe Islands
	case "232", "233", "234", "235":
		return "GB" // United Kingdom
	case "236":
		return "GI" // Gibraltar
	case "237":
		return "GR" // Greece
	case "238":
		return "HR" // Croatia
	case "239":
		return "GR" // Greece
	case "240":
		return "GR" // Greece
	case "241":
		return "GR" // Greece
	case "242":
		return "MA" // Morocco
	case "243":
		return "HU" // Hungary
	case "244", "245", "246":
		return "NL" // Netherlands
	case "247":
		return "IT" // Italy
	case "248":
		return "MT" // Malta
	case "249":
		return "MT" // Malta
	case "250":
		return "IE" // Ireland
	case "251":
		return "IS" // Iceland
	case "252":
		return "LI" // Liechtenstein
	case "253":
		return "LU" // Luxembourg
	case "254":
		return "MC" // Monaco
	case "255":
		return "PT" // Madeira (Portugal)
	case "256":
		return "MT" // Malta
	case "257":
		return "NO" // Norway
	case "258", "259":
		return "NO" // Norway
	case "261":
		return "PL" // Poland
	case "262":
		return "ME" // Montenegro
	case "263":
		return "PT" // Portugal
	case "264":
		return "RO" // Romania
	case "265":
		return "SE" // Sweden
	case "266":
		return "SE" // Sweden
	case "267":
		return "SK" // Slovakia
	case "268":
		return "SM" // San Marino
	case "269":
		return "CH" // Switzerland
	case "270":
		return "CZ" // Czech Republic
	case "271":
		return "TR" // Turkey
	case "272":
		return "UA" // Ukraine
	case "273":
		return "RU" // Russia
	case "274":
		return "MK" // North Macedonia
	case "275":
		return "LV" // Latvia
	case "276":
		return "EE" // Estonia
	case "277":
		return "LT" // Lithuania
	case "278":
		return "SI" // Slovenia
	case "279":
		return "RS" // Serbia
	case "303":
		return "US" // United States (Alaska)
	case "304", "305", "306", "307", "308", "309":
		return "US" // United States
	case "310", "311":
		return "US" // United States
	case "316":
		return "CA" // Canada
	case "338":
		return "US" // United States
	case "341":
		return "KN" // Saint Kitts and Nevis
	case "343":
		return "LC" // Saint Lucia
	case "345":
		return "MX" // Mexico
	case "347":
		return "MQ" // Martinique (France)
	case "348":
		return "MS" // Montserrat
	case "350":
		return "PA" // Panama
	case "351":
		return "PA" // Panama
	case "352":
		return "PA" // Panama
	case "353":
		return "PA" // Panama
	case "354":
		return "PA" // Panama
	case "355":
		return "PA" // Panama
	case "356":
		return "PA" // Panama
	case "357":
		return "PA" // Panama
	case "366":
		return "US" // United States
	case "367":
		return "US" // United States
	case "368", "369":
		return "US" // United States
	case "370", "371", "372", "373", "374":
		return "PA" // Panama
	case "375":
		return "VC" // Saint Vincent
	case "376":
		return "VC" // Saint Vincent
	case "377":
		return "VC" // Saint Vincent
	case "378":
		return "GB" // British Virgin Islands
	case "401":
		return "AF" // Afghanistan
	case "403":
		return "SA" // Saudi Arabia
	case "405":
		return "BD" // Bangladesh
	case "408":
		return "BH" // Bahrain
	case "410":
		return "BT" // Bhutan
	case "412", "413", "414":
		return "CN" // China
	case "416":
		return "TW" // Taiwan
	case "417":
		return "LK" // Sri Lanka
	case "419":
		return "IN" // India
	case "422":
		return "IR" // Iran
	case "423":
		return "AZ" // Azerbaijan
	case "425":
		return "IQ" // Iraq
	case "428":
		return "IL" // Israel
	case "431":
		return "JP" // Japan
	case "432":
		return "JP" // Japan
	case "434":
		return "TM" // Turkmenistan
	case "436":
		return "KZ" // Kazakhstan
	case "437":
		return "UZ" // Uzbekistan
	case "438":
		return "JO" // Jordan
	case "440", "441":
		return "KR" // South Korea
	case "443":
		return "PS" // Palestine
	case "445":
		return "KP" // North Korea
	case "447":
		return "KW" // Kuwait
	case "450":
		return "LB" // Lebanon
	case "451":
		return "KG" // Kyrgyzstan
	case "453":
		return "MO" // Macao (China)
	case "455":
		return "MV" // Maldives
	case "457":
		return "MN" // Mongolia
	case "459":
		return "NP" // Nepal
	case "461":
		return "OM" // Oman
	case "463":
		return "PK" // Pakistan
	case "466":
		return "QA" // Qatar
	case "468":
		return "SY" // Syria
	case "470":
		return "AE" // UAE
	case "471":
		return "AE" // UAE
	case "472":
		return "TJ" // Tajikistan
	case "473":
		return "YE" // Yemen
	case "475":
		return "YE" // Yemen
	case "477":
		return "HK" // Hong Kong
	case "478":
		return "BA" // Bosnia
	case "503":
		return "AU" // Australia
	case "506":
		return "MM" // Myanmar
	case "508":
		return "BN" // Brunei
	case "510":
		return "FM" // Micronesia
	case "511":
		return "PW" // Palau
	case "512":
		return "NZ" // New Zealand
	case "514":
		return "KH" // Cambodia
	case "515":
		return "KH" // Cambodia
	case "516":
		return "CX" // Christmas Island
	case "518":
		return "CK" // Cook Islands
	case "520":
		return "FJ" // Fiji
	case "523":
		return "CC" // Cocos (Keeling) Islands
	case "525":
		return "ID" // Indonesia
	case "529":
		return "KI" // Kiribati
	case "531":
		return "LA" // Laos
	case "533":
		return "MY" // Malaysia
	case "536":
		return "MP" // Northern Mariana Islands
	case "538":
		return "MH" // Marshall Islands
	case "540":
		return "NC" // New Caledonia
	case "542":
		return "NU" // Niue
	case "544":
		return "NR" // Nauru
	case "546":
		return "PF" // French Polynesia
	case "548":
		return "PH" // Philippines
	case "553":
		return "PG" // Papua New Guinea
	case "555":
		return "PN" // Pitcairn Islands
	case "557":
		return "SB" // Solomon Islands
	case "559":
		return "AS" // American Samoa
	case "561":
		return "WS" // Samoa
	case "563":
		return "SG" // Singapore
	case "564":
		return "SG" // Singapore
	case "565":
		return "SG" // Singapore
	case "566":
		return "SG" // Singapore
	case "567":
		return "TH" // Thailand
	case "570":
		return "TO" // Tonga
	case "572":
		return "TV" // Tuvalu
	case "574":
		return "VN" // Vietnam
	case "576":
		return "VU" // Vanuatu
	case "577":
		return "VU" // Vanuatu
	case "578":
		return "WF" // Wallis and Futuna
	case "601":
		return "ZA" // South Africa
	case "603":
		return "AO" // Angola
	case "605":
		return "DZ" // Algeria
	case "607":
		return "SH" // Saint Helena
	case "608":
		return "SH" // Ascension Island
	case "609":
		return "BI" // Burundi
	case "610":
		return "BJ" // Benin
	case "611":
		return "BW" // Botswana
	case "612":
		return "CF" // Central African Republic
	case "613":
		return "CM" // Cameroon
	case "615":
		return "CG" // Congo
	case "616":
		return "KM" // Comoros
	case "617":
		return "CV" // Cape Verde
	case "618":
		return "SH" // Tristan da Cunha
	case "619":
		return "CI" // Côte d'Ivoire
	case "620":
		return "KM" // Comoros
	case "621":
		return "DJ" // Djibouti
	case "622":
		return "EG" // Egypt
	case "624":
		return "ET" // Ethiopia
	case "625":
		return "ER" // Eritrea
	case "626":
		return "GA" // Gabon
	case "627":
		return "GH" // Ghana
	case "629":
		return "GM" // Gambia
	case "630":
		return "GW" // Guinea-Bissau
	case "631":
		return "GQ" // Equatorial Guinea
	case "632":
		return "GN" // Guinea
	case "633":
		return "BF" // Burkina Faso
	case "634":
		return "KE" // Kenya
	case "635":
		return "KE" // Kenya
	case "636":
		return "LR" // Liberia
	case "637":
		return "LR" // Liberia
	case "638":
		return "SS" // South Sudan
	case "642":
		return "LY" // Libya
	case "644":
		return "LS" // Lesotho
	case "645":
		return "MU" // Mauritius
	case "647":
		return "MG" // Madagascar
	case "649":
		return "ML" // Mali
	case "650":
		return "MZ" // Mozambique
	case "654":
		return "MR" // Mauritania
	case "655":
		return "MW" // Malawi
	case "656":
		return "NE" // Niger
	case "657":
		return "NG" // Nigeria
	case "659":
		return "NA" // Namibia
	case "660":
		return "RE" // Réunion (France)
	case "661":
		return "RW" // Rwanda
	case "662":
		return "SD" // Sudan
	case "663":
		return "SN" // Senegal
	case "664":
		return "SC" // Seychelles
	case "665":
		return "SH" // Saint Helena
	case "666":
		return "SO" // Somalia
	case "667":
		return "SL" // Sierra Leone
	case "668":
		return "ST" // São Tomé and Príncipe
	case "669":
		return "SZ" // Eswatini
	case "670":
		return "TD" // Chad
	case "671":
		return "TG" // Togo
	case "672":
		return "TN" // Tunisia
	case "674":
		return "TZ" // Tanzania
	case "675":
		return "UG" // Uganda
	case "676":
		return "CD" // DR Congo
	case "677":
		return "TZ" // Tanzania
	case "678":
		return "ZM" // Zambia
	case "679":
		return "ZW" // Zimbabwe
	case "701":
		return "AR" // Argentina
	case "710":
		return "BR" // Brazil
	case "720":
		return "BO" // Bolivia
	case "725":
		return "CL" // Chile
	case "730":
		return "CO" // Colombia
	case "735":
		return "CR" // Costa Rica
	case "740":
		return "EC" // Ecuador
	case "745":
		return "GT" // Guatemala
	case "750":
		return "GY" // Guyana
	case "755":
		return "HN" // Honduras
	case "760":
		return "PE" // Peru
	case "765":
		return "SR" // Suriname
	case "770":
		return "UY" // Uruguay
	case "775":
		return "VE" // Venezuela
	default:
		return ""
	}
}
