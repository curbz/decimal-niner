package atc

import (
	"testing"

	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
)

func TestCleanAirportName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Aeropuerto Internacional (Benito Juárez) [MEX]", "benito juarez"},
		{"Foo Intl Bar", "foo bar"},
		{"Valencia / Manises", "manises"},
		{"Not This/Preferred", "preferred"},
		{"City - Terminal", "city"},
		{"A y B", "a e b"},
		{"São Paulo", "sao paulo"},
		{"Airport Name (Preferred)", "preferred"},
		{"Aéroport (Éole)", "eole"},
		{"[H] Some Helipad", "some helipad"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := cleanAirportName(tt.in)
			if got != tt.want {
				t.Fatalf("cleanAirportName(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLowestAltitudeOf(t *testing.T) {
	tests := []struct {
		name             string
		at, above, below string
		want             int
	}{
		{"all empty", "", "", "", -1},
		{"only at", "100", "", "", 100},
		{"above and below", "", "200", "150", 150},
		{"invalid at", "foo", "50", "25", 25},
		{"multiple", "300", "200", "400", 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lowestAltitudeOf(tt.at, tt.above, tt.below)
			if got != tt.want {
				t.Fatalf("lowestAltitudeOf(%q,%q,%q) = %d; want %d", tt.at, tt.above, tt.below, got, tt.want)
			}
		})
	}
}

func TestNormaliseRunway(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"RW08", "08"},
		{"RW8", "08"},
		{"RW27L", "27L"},
		{"RW9C", "09C"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := normaliseRunway(tt.in)
			if got != tt.want {
				t.Fatalf("normalizeRunway(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestGetAirportRunway(t *testing.T) {
	rwy := &Runway{FAFalt: 123}
	s := &Service{Airports: map[string]*Airport{
		"TEST": {ICAO: "TEST", Runways: map[string]*Runway{"09L": rwy}},
	}}

	tests := []struct {
		name, icao, rwy string
		wantPresent     bool
	}{
		{"present", "TEST", "09L", true},
		{"absent runway", "TEST", "09R", false},
		{"empty icao", "", "09L", false},
		{"missing airport", "NOPE", "09L", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.getAirportRunway(tt.icao, tt.rwy)
			if tt.wantPresent {
				if got == nil {
					t.Fatalf("expected runway present for %s/%s", tt.icao, tt.rwy)
				}
				if got.FAFalt != 123 {
					t.Fatalf("unexpected runway data: FAFalt=%d", got.FAFalt)
				}
			} else {
				if got != nil {
					t.Fatalf("expected nil runway for %s/%s, got %+v", tt.icao, tt.rwy, got)
				}
			}
		})
	}
}

func TestGetAirportICAObyPhaseClass(t *testing.T) {
	tests := []struct {
		name         string
		class        flightclass.PhaseClass
		origin, dest string
		want         string
	}{
		{"preflight", flightclass.PreflightParked, "AAA", "BBB", "AAA"},
		{"departing", flightclass.Departing, "AAA", "BBB", "AAA"},
		{"cruising", flightclass.Cruising, "AAA", "BBB", ""},
		{"arriving", flightclass.Arriving, "AAA", "BBB", "BBB"},
		{"postflight", flightclass.PostflightParked, "AAA", "BBB", "BBB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ac := &Aircraft{Flight: Flight{Origin: tt.origin, Destination: tt.dest, Phase: flightphase.Phase{Class: tt.class}}}
			got := getAirportICAObyPhaseClass(ac)
			if got != tt.want {
				t.Fatalf("getAirportICAObyPhaseClass() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestGetClosestAirport(t *testing.T) {
	s := &Service{Airports: map[string]*Airport{
		"AAA": {ICAO: "AAA", Lat: 10.0, Lon: 10.0},
		"BBB": {ICAO: "BBB", Lat: 20.0, Lon: 20.0},
	}}

	tests := []struct {
		name     string
		lat, lon, maxRangeNm float64
		want     string
	}{
		{"near AAA", 9.0, 11.0, 100.0, "AAA"},
		{"near BBB", 15.0, 30.0, 1200.0, "BBB"},
		{"near none", 50.0, -50.0, 2000.0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.GetClosestAirport(tt.lat, tt.lon, tt.maxRangeNm)
			if got != tt.want {
				t.Fatalf("GetClosestAirport(%f,%f) = %q; want %q", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
}
