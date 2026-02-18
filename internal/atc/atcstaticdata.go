package atc

import "github.com/curbz/decimal-niner/internal/trafficglobal"

var SizeClass = []string {
	"A", "B", "C", "D", "E", "F",
}

// icaoToIsoMap contains the comprehensive list of ICAO nationality
// prefixes mapped to ISO 3166-1 alpha-2 country codes.
var icaoToIsoMap = map[string]string{
	// --- 1-Letter Major Prefixes ---
	"C": "CA", // Canada
	"K": "US", // United States (Contiguous)
	"Y": "AU", // Australia
	"Z": "CN", // China

	// --- 2-Letter Prefixes (Alphabetical by ICAO) ---
	"AG": "SB", // Solomon Islands
	"AN": "NR", // Nauru
	"AY": "PG", // Papua New Guinea
	"BG": "GL", // Greenland
	"BI": "IS", // Iceland
	"BK": "XK", // Kosovo (User-defined ISO code often used)
	"DA": "DZ", // Algeria
	"DB": "BJ", // Benin
	"DF": "BF", // Burkina Faso
	"DG": "GH", // Ghana
	"DI": "CI", // Côte d'Ivoire
	"DN": "NG", // Nigeria
	"DR": "NE", // Niger
	"DT": "TN", // Tunisia
	"DX": "TG", // Togo
	"EB": "BE", // Belgium
	"ED": "DE", // Germany (Civil)
	"EE": "EE", // Estonia
	"EF": "FI", // Finland
	"EG": "GB", // United Kingdom
	"EH": "NL", // Netherlands
	"EI": "IE", // Ireland
	"EK": "DK", // Denmark
	"EL": "LU", // Luxembourg
	"EN": "NO", // Norway
	"EP": "PL", // Poland
	"ES": "SE", // Sweden
	"ET": "DE", // Germany (Military)
	"EV": "LV", // Latvia
	"EY": "LT", // Lithuania
	"FA": "ZA", // South Africa
	"FB": "BW", // Botswana
	"FC": "CG", // Congo (Republic)
	"FD": "SZ", // Eswatini (Swaziland)
	"FE": "CF", // Central African Republic
	"FG": "GQ", // Equatorial Guinea
	"FH": "SH", // Saint Helena
	"FI": "MU", // Mauritius
	"FJ": "IO", // British Indian Ocean Territory
	"FK": "CM", // Cameroon
	"FL": "ZM", // Zambia
	"FM": "MG", // Madagascar (also Comoros/Mayotte/Reunion)
	"FN": "AO", // Angola
	"FO": "GA", // Gabon
	"FP": "ST", // Sao Tome and Principe
	"FQ": "MZ", // Mozambique
	"FS": "SC", // Seychelles
	"FT": "TD", // Chad
	"FV": "ZW", // Zimbabwe
	"FW": "MW", // Malawi
	"FX": "LS", // Lesotho
	"FY": "NA", // Namibia
	"FZ": "CD", // Congo (Democratic Republic)
	"GA": "ML", // Mali
	"GB": "GM", // Gambia
	"GC": "ES", // Spain (Canary Islands)
	"GE": "ES", // Spain (Ceuta/Melilla)
	"GF": "SL", // Sierra Leone
	"GG": "GW", // Guinea-Bissau
	"GL": "LR", // Liberia
	"GM": "MA", // Morocco
	"GO": "SN", // Senegal
	"GQ": "MR", // Mauritania
	"GU": "GN", // Guinea
	"GV": "CV", // Cape Verde
	"HA": "ET", // Ethiopia
	"HB": "BI", // Burundi
	"HC": "SO", // Somalia
	"HD": "DJ", // Djibouti
	"HE": "EG", // Egypt
	"HH": "ER", // Eritrea
	"HJ": "SS", // South Sudan
	"HK": "KE", // Kenya
	"HL": "LY", // Libya
	"HR": "RW", // Rwanda
	"HS": "SD", // Sudan
	"HT": "TZ", // Tanzania
	"HU": "UG", // Uganda
	"LA": "AL", // Albania
	"LB": "BG", // Bulgaria
	"LC": "CY", // Cyprus
	"LD": "HR", // Croatia
	"LE": "ES", // Spain (Mainland/Balearic)
	"LF": "FR", // France (Metropolitan/St. Pierre)
	"LG": "GR", // Greece
	"LH": "HU", // Hungary
	"LI": "IT", // Italy
	"LJ": "SI", // Slovenia
	"LK": "CZ", // Czech Republic
	"LL": "IL", // Israel
	"LM": "MT", // Malta
	"LN": "MC", // Monaco
	"LO": "AT", // Austria
	"LP": "PT", // Portugal
	"LQ": "BA", // Bosnia and Herzegovina
	"LR": "RO", // Romania
	"LS": "CH", // Switzerland
	"LT": "TR", // Turkey
	"LU": "MD", // Moldova
	"LV": "PS", // Palestine
	"LW": "MK", // North Macedonia
	"LX": "GI", // Gibraltar
	"LY": "RS", // Serbia (also Montenegro)
	"LZ": "SK", // Slovakia
	"MB": "TC", // Turks and Caicos
	"MD": "DO", // Dominican Republic
	"MG": "GT", // Guatemala
	"MH": "HN", // Honduras
	"MK": "JM", // Jamaica
	"MM": "MX", // Mexico
	"MN": "NI", // Nicaragua
	"MP": "PA", // Panama
	"MR": "CR", // Costa Rica
	"MS": "SV", // El Salvador
	"MT": "HT", // Haiti
	"MU": "CU", // Cuba
	"MW": "KY", // Cayman Islands
	"MY": "BS", // Bahamas
	"MZ": "BZ", // Belize
	"NC": "CK", // Cook Islands
	"NF": "FJ", // Fiji (also Tonga)
	"NG": "KI", // Kiribati (also Tuvalu)
	"NI": "NU", // Niue
	"NL": "WF", // Wallis and Futuna
	"NS": "WS", // Samoa (also American Samoa)
	"NT": "PF", // French Polynesia
	"NV": "VU", // Vanuatu
	"NW": "NC", // New Caledonia
	"NZ": "NZ", // New Zealand
	"OA": "AF", // Afghanistan
	"OB": "BH", // Bahrain
	"OE": "SA", // Saudi Arabia
	"OI": "IR", // Iran
	"OJ": "JO", // Jordan
	"OK": "KW", // Kuwait
	"OL": "LB", // Lebanon
	"OM": "AE", // United Arab Emirates
	"OO": "OM", // Oman
	"OP": "PK", // Pakistan
	"OR": "IQ", // Iraq
	"OS": "SY", // Syria
	"OT": "QA", // Qatar
	"OY": "YE", // Yemen
	"PA": "US", // Alaska (USA)
	"PG": "GU", // Guam (USA)
	"PH": "US", // Hawaii (USA)
	"PJ": "UM", // Johnston Atoll (USA Minor Islands)
	"PK": "MH", // Marshall Islands
	"PL": "KI", // Line Islands (Kiribati)
	"PM": "UM", // Midway Island (USA Minor Islands)
	"PT": "FM", // Micronesia
	"PW": "UM", // Wake Island (USA Minor Islands)
	"RC": "TW", // Taiwan
	"RJ": "JP", // Japan
	"RK": "KR", // South Korea
	"RO": "JP", // Okinawa (Japan)
	"RP": "PH", // Philippines
	"SA": "AR", // Argentina
	"SB": "BR", // Brazil
	"SC": "CL", // Chile
	"SE": "EC", // Ecuador
	"SF": "FK", // Falkland Islands
	"SG": "PY", // Paraguay
	"SK": "CO", // Colombia
	"SL": "BO", // Bolivia
	"SM": "SR", // Suriname
	"SO": "GF", // French Guiana
	"SP": "PE", // Peru
	"SU": "UY", // Uruguay
	"SV": "VE", // Venezuela
	"SY": "GY", // Guyana
	"TA": "AG", // Antigua and Barbuda
	"TB": "BB", // Barbados
	"TD": "DM", // Dominica
	"TF": "GP", // French Antilles (Guadeloupe/Martinique)
	"TG": "GD", // Grenada
	"TI": "VI", // U.S. Virgin Islands
	"TJ": "PR", // Puerto Rico
	"TK": "KN", // St. Kitts and Nevis
	"TL": "LC", // St. Lucia
	"TN": "AW", // Aruba (also Curacao/Bonaire)
	"TQ": "AI", // Anguilla
	"TT": "MS", // Montserrat
	"TU": "TT", // Trinidad and Tobago
	"TV": "VG", // British Virgin Islands
	"TX": "BM", // Bermuda
	"UA": "KZ", // Kazakhstan
	"UB": "AZ", // Azerbaijan
	"UD": "AM", // Armenia
	"UE": "RU", // Russia (East)
	"UG": "GE", // Georgia
	"UH": "RU", // Russia (Far East)
	"UK": "UA", // Ukraine
	"UL": "RU", // Russia (Northwest)
	"UM": "BY", // Belarus
	"UN": "RU", // Russia (Central)
	"UR": "RU", // Russia (South)
	"US": "RU", // Russia (Siberia)
	"UT": "UZ", // Uzbekistan (also Turkmenistan/Tajikistan)
	"UU": "RU", // Russia (Moscow region)
	"UW": "RU", // Russia (Volga region)
	"VA": "IN", // India (West)
	"VC": "LK", // Sri Lanka
	"VD": "KH", // Cambodia
	"VE": "IN", // India (East)
	"VG": "BD", // Bangladesh
	"VH": "HK", // Hong Kong
	"VI": "IN", // India (North)
	"VL": "LA", // Laos
	"VM": "MO", // Macau
	"VN": "NP", // Nepal
	"VO": "IN", // India (South)
	"VQ": "BT", // Bhutan
	"VR": "MV", // Maldives
	"VT": "TH", // Thailand
	"VV": "VN", // Vietnam
	"VY": "MM", // Myanmar
	"WA": "ID", // Indonesia
	"WB": "BN", // Brunei
	"WM": "MY", // Malaysia
	"WS": "SG", // Singapore
	"ZK": "KP", // North Korea
	"ZM": "MN", // Mongolia
}

var phoneticMap = map[string]string{
	"A": "Alpha", "B": "Bravo", "C": "Charlie", "D": "Delta", "E": "Echo",
	"F": "Foxtrot", "G": "Golf", "H": "Hotel", "I": "India", "J": "Juliett",
	"K": "Kilo", "L": "Lima", "M": "Mike", "N": "November", "O": "Oscar",
	"P": "Papa", "Q": "Quebec", "R": "Romeo", "S": "Sierra", "T": "Tango",
	"U": "Uniform", "V": "Victor", "W": "Whiskey", "X": "X-ray", "Y": "Yankee",
	"Z": "Zulu", "0": "Zero", "1": "One", "2": "Two", "3": "Tree",
	"4": "Fower", "5": "Fife", "6": "Six", "7": "Seven", "8": "Eight", "9": "Niner",
}

var numericMap = map[rune]string{
	'0': "zero",
	'1': "one",
	'2': "two",
	'3': "three",
	'4': "four",
	'5': "five",
	'6': "six",
	'7': "seven",
	'8': "eight",
	'9': "niner",
}

var atcFacilityByPhaseMap = map[trafficglobal.FlightPhase]PhaseFacility {
	// PRE-FLIGHT & DEPARTURE
	trafficglobal.Parked: {
		atcPhase: "pre_flight_parked",
		roleId: 1,
	},
	trafficglobal.Startup: {
		atcPhase: "startup",
		roleId: 2,
	},
	trafficglobal.TaxiOut: {
		atcPhase: "taxi_out",
		roleId: 2,
	},
	trafficglobal.Depart: {
		atcPhase: "depart",
		roleId: 3,
	},
	trafficglobal.Climbout: {
		atcPhase: "climb_out",
		roleId: 4,
	},
	// --- ENROUTE & ARRIVAL ---
	trafficglobal.Cruise: {
		atcPhase: "cruise",
		roleId: 6,
	},
	trafficglobal.Approach: {
		atcPhase: "approach",
		roleId: 5,
	},
	trafficglobal.Holding: {
		atcPhase: "holding",
		roleId: 5,
	},
	trafficglobal.Final: {
		atcPhase: "final",
		roleId: 3,
	},
	trafficglobal.GoAround: {
		atcPhase: "go_around",
		roleId: 3,
	},
	// --- LANDING & TAXI-IN ---
	trafficglobal.Braking: {
		// In Traffic Global, Braking usually covers the rollout and runway exit
		atcPhase: "braking",
		roleId: 3,
	},
	trafficglobal.TaxiIn: {
		atcPhase: "taxi_in",
		roleId: 2,
	},
	trafficglobal.Shutdown: {
		atcPhase: "post_flight_parked",
		roleId: 2,
	},
}

var roleNameMap = map[int]string {
	0: "None",
	1: "Delivery",
	2: "Ground",
	3: "Tower",
	4: "Departure",
	5: "Approach",
	6: "Center",
	7: "Flight Service",
	8: "AWOS/ASOS/ATIS",
	9: "Unicom",  //TODO: shoud Unicom be 0?
}

var handoffMap = map[trafficglobal.FlightPhase]int{
    trafficglobal.Parked: 	2, // Delivery -> Ground
    trafficglobal.TaxiOut:  3, // Ground -> Tower
    trafficglobal.Depart:   4, // Tower -> Departure
    trafficglobal.Climbout: 6, // Departure -> Center
    trafficglobal.Cruise:   5, // Center -> Approach (or another Center)
    trafficglobal.Approach: 3, // Approach -> Tower
    trafficglobal.Braking:  2, // Tower -> Ground
}

