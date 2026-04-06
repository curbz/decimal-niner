package atc

import (
	"strconv"
	"strings"
	"testing"

	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/simdata"
)

func TestScaleAltitude(t *testing.T) {
	tests := []struct {
		name            string
		rawAlt          float64
		transitionLevel int
		phaseCurrent    int
		wantVal         int
		wantIsFL        bool
	}{
		{
			name:            "approach rounds to hundreds and returns feet",
			rawAlt:          2412,
			transitionLevel: 200, // 200*100 = 20000 threshold
			phaseCurrent:    flightphase.Approach.Index(),
			wantVal:         2400,
			wantIsFL:        false,
		},
		{
			name:            "default rounds to thousands and remains feet below transition",
			rawAlt:          3240,
			transitionLevel: 200, // 20000
			phaseCurrent:    flightphase.Unknown.Index(),
			wantVal:         3000,
			wantIsFL:        false,
		},
		{
			name:            "default rounds to thousands and becomes flight level above transition",
			rawAlt:          33240,
			transitionLevel: 200, // 20000
			phaseCurrent:    flightphase.Unknown.Index(),
			wantVal:         330, // 33000 -> /100 = 330
			wantIsFL:        true,
		},
		{
			name:            ">=18000 becomes flight level even if transition is higher",
			rawAlt:          18001,
			transitionLevel: 500, // 50000 so threshold not reached, but 18000 rule applies
			phaseCurrent:    flightphase.Unknown.Index(),
			wantVal:         180, // 18000 -> /100
			wantIsFL:        true,
		},
		{
			name:            "cruise flight level is a multiple of 10",
			rawAlt:          33499,
			transitionLevel: 200,
			phaseCurrent:    flightphase.Cruise.Index(),
			// rounded -> ((33499+500)/1000)*1000 = 33000 -> fl 330 (already multiple of 10)
			wantVal:  330,
			wantIsFL: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := flightphase.Phase{Current: tt.phaseCurrent}
			gotVal, gotFL := scaleAltitude(tt.rawAlt, tt.transitionLevel, ph)
			if gotVal != tt.wantVal || gotFL != tt.wantIsFL {
				t.Fatalf("%s: scaleAltitude(%v,%d,phase) = (%d,%v); want (%d,%v)", tt.name, tt.rawAlt, tt.transitionLevel, gotVal, gotFL, tt.wantVal, tt.wantIsFL)
			}
		})
	}
}

func TestAutoReadback(t *testing.T) {
	in := "{$CALLSIGN}, hello [not this]there."
	want := " hello there {$CALLSIGN}"
	got := autoReadback(in)
	if got != want {
		t.Fatalf("autoReadback(%q) = %q; want %q", in, got, want)
	}
}

func TestTranslateNumerics(t *testing.T) {
	tests := []struct{ in, want string }{
		{"123", " one two three "},
		{"A1B2", "A one B two "},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := translateNumerics(tt.in)
			if got != tt.want {
				t.Fatalf("translateNumerics(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTranslateRunway(t *testing.T) {
	tests := []struct{ in, want string }{
		{"09L", "09left"},
		{"27R", "27right"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := translateRunway(tt.in)
			if got != tt.want {
				t.Fatalf("translateRunway(%q)=%q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatBaro(t *testing.T) {
	// North America -> altimeter inHg without dot
	got := formatBaro(101325.0, true)
	if got != "altimeter 2992" {
		t.Fatalf("formatBaro(K...) = %q; want prefix altimeter", got)
	}

	// non-North America -> QNH hPa
	got2 := formatBaro(101325.0, false)
	if got2 != "QNH 1013" {
		t.Fatalf("formatBaro(E...) = %q; want QNH 1013", got2)
	}
}

func TestGenerateAltClearance(t *testing.T) {
	tests := []struct {
		name            string
		rawAlt          float64
		transitionLevel int
		clearance       int
		phaseCurrent    int
		wantPrefix      string
		wantContains    string
	}{
		{
			name:            "climb to feet",
			rawAlt:          1000,
			transitionLevel: 200,
			clearance:       3000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "climb to",
			wantContains:    "thousand",
		},
		{
			name:            "maintain feet",
			rawAlt:          3000,
			transitionLevel: 200,
			clearance:       3000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "maintain",
			wantContains:    "thousand",
		},
		{
			name:            "descend to feet",
			rawAlt:          3000,
			transitionLevel: 200,
			clearance:       2000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "descend to",
			wantContains:    "thousand",
		},
		{
			name:            "maintain flight level",
			rawAlt:          33240,
			transitionLevel: 200,
			clearance:       33000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "maintain",
			wantContains:    "flight level",
		},
		{
			name:            "descend to flight level",
			rawAlt:          35000,
			transitionLevel: 200,
			clearance:       33000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "descend to",
			wantContains:    "flight level",
		},
		{
			name:            "climb to flight level from feet",
			rawAlt:          5000,
			transitionLevel: 200,
			clearance:       33000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "climb to",
			wantContains:    "flight level",
		},
		{
			name:            "descend to feet from flight level",
			rawAlt:          33000,
			transitionLevel: 200,
			clearance:       5000,
			phaseCurrent:    flightphase.Unknown.Index(),
			wantPrefix:      "descend to",
			wantContains:    "thousand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := flightphase.Phase{Current: tt.phaseCurrent}
			got := generateAltClearance(tt.rawAlt, tt.transitionLevel, tt.clearance, ph)
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Fatalf("%s: generateAltClearance -> %q; want prefix %q", tt.name, got, tt.wantPrefix)
			}
			if tt.wantContains != "" && !strings.Contains(got, tt.wantContains) {
				t.Fatalf("%s: generateAltClearance -> %q; want to contain %q", tt.name, got, tt.wantContains)
			}
		})
	}
}

func TestFormatParkingAndPhoneticise(t *testing.T) {
	// empty
	if p := formatParking("", false); p != "parking" {
		t.Fatalf("formatParking empty = %q; want parking", p)
	}

	// numeric with suffix for North American airports
	p := formatParking("201R", true)
	if !strings.HasPrefix(p, "gate 201") {
		t.Fatalf("formatParking(K) = %q; want prefix gate 201", p)
	}

	// numeric with suffix for non-North American airports
	p = formatParking("201R", false)
	if !strings.HasPrefix(p, "stand 201") {
		t.Fatalf("formatParking(K) = %q; want prefix stand 201", p)
	}

	// phoneticise single alphas
	got := phoneticiseSingleAlphas("Ramp A")
	if got != "ramp alpha" {
		t.Fatalf("phoneticiseSingleAlphas = %q; want ramp alpha", got)
	}
}

func TestFormatAirportNameAndToPhonetics(t *testing.T) {
	m := map[string]*Airport{"TEST": {ICAO: "TEST", Name: "Foo Intl Airport"}}
	got := formatAirportName("TEST", m)
	if got != "Foo" {
		t.Fatalf("formatAirportName = %q; want Foo", got)
	}

	// toPhonetics
	got2 := toPhonetics("EGLL")
	if got2 != "Echo Golf Lima Lima" {
		t.Fatalf("toPhonetics AB = %q; want Echo Golf Lima Lima", got2)
	}
}

// mock simdata provider
type mockDP struct{ LocalSecs float64 }

func (m mockDP) GetSimTime() (simdata.XPlaneTime, error) {
	return simdata.XPlaneTime{LocalTimeSecs: m.LocalSecs}, nil
}

func TestGenerateHandoffPhraseAndValediction(t *testing.T) {
	s := &Service{}
	s.Config = &config{}
	s.Config.ATC.Voices.HandoffValedictionFactor = 1
	s.DataProvider = mockDP{LocalSecs: float64(10 * 3600)} // 10:00 -> good day

	// controller to be found
	ctrl := &Controller{ICAO: "NEXT", Name: "NextCtrl", RoleID: 4, IsPoint: true, Lat: 51.0, Lon: -0.1, Freqs: []int{118500}}
	s.Controllers = []*Controller{ctrl}

	ac := &Aircraft{Flight: Flight{Position: Position{Lat: 51.0, Long: -0.1}, Phase: flightphase.Phase{Current: flightphase.Depart.Index()}}}

	ph := s.generateHandoffPhrase(ac)
	if ph == "" {
		t.Fatalf("generateHandoffPhrase returned empty")
	}
	if !strings.Contains(ph, "118.500") && !strings.Contains(ph, "118 decimal 5") {
		t.Fatalf("generateHandoffPhrase missing freq: %q", ph)
	}
	if !strings.Contains(ph, "good day") {
		t.Fatalf("generateHandoffPhrase missing valediction: %q", ph)
	}
}

func TestGenerateValedictionTableDriven(t *testing.T) {
	tests := []struct {
		name string
		hour int
		want string
	}{
		{"good day", 10, "good day"},
		{"good evening", 19, "good evening"},
		{"good night", 23, "good night"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{}
			s.DataProvider = mockDP{LocalSecs: float64(tt.hour * 3600)}
			v := s.generateValediction(1)
			if !strings.Contains(v, tt.want) {
				t.Fatalf("generateValediction(%d) = %q; want contain %q", tt.hour, v, tt.want)
			}
		})
	}
}

func TestFormatWindVariants(t *testing.T) {
	tests := []struct {
		name         string
		weather      *Weather
		wantExact    string
		wantContains string
	}{
		{
			name:      "calm",
			weather:   &Weather{Wind: &Wind{Direction: 10, Speed: 1.0}, Turbulence: 0},
			wantExact: "calm",
		},
		{
			name:         "gusting",
			weather:      &Weather{Wind: &Wind{Direction: 350, Speed: 20.0}, Turbulence: 0.5, MagVar: 0},
			wantContains: "gusting",
		},
		{
			name:      "direction rounds to 360 and reports knots",
			weather:   &Weather{Wind: &Wind{Direction: 2, Speed: 6.2}, Turbulence: 0, MagVar: 0},
			wantExact: "360 at 12 knots",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{Weather: tt.weather}
			got := s.formatWind()
			if tt.wantExact != "" {
				if got != tt.wantExact {
					t.Fatalf("formatWind %s = %q; want %q", tt.name, got, tt.wantExact)
				}
			}
			if tt.wantContains != "" {
				if !strings.Contains(got, tt.wantContains) {
					t.Fatalf("formatWind %s expected %q, got %q", tt.name, tt.wantContains, got)
				}
			}
		})
	}
}

func TestFormatWindShear(t *testing.T) {
	tests := []struct {
		name        string
		shear       float64
		wantMention bool
		wantValue   string
	}{
		{"significant shear", 8.0, true, "15"}, // ~15.55 kt
		{"small shear", 1.0, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{Weather: &Weather{Wind: &Wind{Shear: tt.shear}}}
			got := s.formatWindShear()

			if tt.wantMention {
				if !strings.Contains(strings.ToLower(got), "wind shear") {
					t.Fatalf("expected mention of wind shear, got %q", got)
				}

				if !strings.Contains(got, "knots") {
					t.Fatalf("expected knots unit in %q", got)
				}

				if tt.wantValue != "" && !strings.Contains(got, tt.wantValue) {
					t.Fatalf("expected value %q in %q", tt.wantValue, got)
				}
			} else {
				if got != "" {
					t.Fatalf("expected empty for small shear, got %q", got)
				}
			}
		})
	}
}

func TestFormatTurbulence(t *testing.T) {
	s := &Service{Weather: &Weather{Turbulence: 0.8}}
	if s.formatTurbulence("PILOT") != "experiencing severe turbulence" {
		t.Fatalf("formatTurbulence severe pilot phrasing mismatch")
	}
	if s.formatTurbulence("ATC") != "severe turbulence [reported]" {
		t.Fatalf("formatTurbulence severe ATC phrasing mismatch")
	}
}

func TestCleanPhrase(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"accents", "Café", "Cafe"},
		{"brackets", "Hello [test]", "Hello test"},
		{"braces_parentheses", "{CALLSIGN}, hello (world)", "CALLSIGN, hello world"},
		{"ellipsis", "Wait...", "Wait."},
		{"trim_comma", "Hello,", "Hello"},
		{"remove_special", "Good %day ©", "Good day"},
		{"trim_spaces_and_dots", "  test  ...", "test."},
		{"hyphen_preserved", "Gate A-12", "Gate A-12"},
		{"dots_inside", "abc.def", "abc.def"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanPhrase(tc.in)
			if got != tc.want {
				t.Fatalf("cleanPhrase(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatAltitude(t *testing.T) {
	tests := []struct {
		name            string
		rawAlt          float64
		transitionLevel int
		phaseCurrent    int
		want            string
	}{
		{
			name:            "flight level above transition",
			rawAlt:          33240,
			transitionLevel: 200,
			phaseCurrent:    flightphase.Unknown.Index(),
			want:            "flight level 330",
		},
		{
			name:            "feet clean thousand",
			rawAlt:          5000,
			transitionLevel: 200,
			phaseCurrent:    flightphase.Unknown.Index(),
			want:            "5 thousand",
		},
		{
			name:            "approach split hundreds",
			rawAlt:          2412,
			transitionLevel: 200,
			phaseCurrent:    flightphase.Approach.Index(),
			want:            "2 thousand 4 hundred",
		},
		{
			name:            "below transition remains feet",
			rawAlt:          3240,
			transitionLevel: 400, // higher transition so stays feet
			phaseCurrent:    flightphase.Unknown.Index(),
			want:            "3 thousand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := flightphase.Phase{Current: tt.phaseCurrent}
			got := formatAltitude(tt.rawAlt, tt.transitionLevel, ph)
			if got != tt.want {
				t.Fatalf("%s: formatAltitude(...) = %q; want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestFormatFrequency(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{118050, "118 decimal 05"},
		{118255, "118 decimal 255"},
		{121900, "121 decimal 9"},
		{135000, "135 decimal 0"},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.in), func(t *testing.T) {
			got := formatFrequency(tt.in)
			if got != tt.want {
				t.Fatalf("formatFrequency(%d) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPCL_StressFallbacks(t *testing.T) {
	s := &Service{
		// Mock weather initialized as per your start-up logic
		Weather: &Weather{
			Baro: &Baro{Sealevel: 101325, Flight: 101325},
			Wind: &Wind{Speed: 0, Direction: 0},
		},
	}

	// Case 1: The "Ghost" Aircraft (No Controller, No Runway)
	acGhost := &Aircraft{
		Registration: "G-HOST",
		Flight: Flight{
			Comms: Comms{
				Callsign:   "GHOST1",
				Controller: nil, // This is the primary panic risk
			},
			Position: Position{Lat: 51.47, Long: -0.45, Altitude: 2000, Heading: 270},
			Phase:    flightphase.Phase{Current: 0}, // Pre-flight/Shutdown
		},
	}

	t.Run("Controller Nil Safety", func(t *testing.T) {
		// Ensure this doesn't panic
		ctx := s.newPCLContext(acGhost, "PILOT")

		// Test @BARO fallback logic
		baroFunc := ctx["@BARO"]
		res := baroFunc().(string)
		if res == "" {
			t.Error("@BARO returned empty string; expected formatted default")
		}

		// Test @HOLD_FIX fallback logic
		holdFunc := ctx["@HOLD_FIX"]
		if holdFunc() != "published hold" {
			t.Errorf("Expected 'published hold' for nil controller, got %v", holdFunc())
		}
	})

	// Case 2: The "Handoff" Aircraft (Next Controller set, current is nil)
	acHandoff := &Aircraft{
		Registration: "N123",
		Flight: Flight{
			Comms: Comms{
				Controller:     nil,
				NextController: &Controller{ICAO: "EGLL", Name: "London"},
			},
		},
	}

	t.Run("Handoff Logic Safety", func(t *testing.T) {
		ctx := s.newPCLContext(acHandoff, "PILOT")

		// Ensure $FACILITY handles nil Current Controller
		facilityFunc := ctx["$FACILITY"]
		if facilityFunc() != "" {
			t.Errorf("Expected empty facility name, got %v", facilityFunc())
		}
	})
}
