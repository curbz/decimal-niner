package atc

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

func init() {
	// This runs before any tests in this package
	// We move up two levels to the root of the repo so that config.yaml and /resources is found
	_ = os.Chdir("../../")
}

func TestPerformSearch(t *testing.T) {

	atcService := New("config.yaml", make(map[string][]trafficglobal.ScheduledFlight))

	tests := []struct {
		label      string
		f, r       int
		la, lo, al float64
		icao       string
	}{
		{"Heathrow Tower (Freq Match)", 118505, 3, 51.4706, -0.4522, 1000.0, ""},
		{"London Center (Polygon Match)", 121520, 5, 51.5, -0.1, 20000.0, ""},
		{"Shoreham Ground (Proximity Match)", 0, 2, 50.835, -0.297, 50.0, ""},
	}

	for _, t := range tests {
		m := atcService.LocateController(t.label, t.f, t.r, t.la, t.lo, t.al, t.icao)
		if m != nil {
			fmt.Printf("FINAL RESULT: %s (%s)\n\n", m.Name, m.ICAO)
		} else {
			fmt.Printf("FINAL RESULT: NO MATCH\n\n")
		}
	}

}

//TODO: these tests must use a config.yaml that has use_flightplan_fallback set to false. Write more tests for when true.
func TestAddFlightPlan(t *testing.T) {
	tests := []struct {
		name          string
		registration  string
		flightNumber  int
		simTime       time.Time
		schedules     map[string][]trafficglobal.ScheduledFlight
		expectOrigin  string
		expectDest    string
		expectNoMatch bool
	}{
		{
			name:         "Match today's flight within time window",
			registration: "N12345",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 10:30
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N12345_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 0, // Monday
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			expectOrigin:  "KJFK",
			expectDest:    "KLAX",
			expectNoMatch: false,
		},
		{
			name:         "Match today's flight in extended arrival time window",
			registration: "N12346",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 13, 15, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 13:15
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N12346_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 0,
						ArrivalHour:        13,  // actual scheduled arrival is 13:15
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			expectOrigin:  "KJFK",
			expectDest:    "KLAX",
			expectNoMatch: false,
		},
		{
			name:         "Match today's flight in extended departure time window",
			registration: "N12347",
			flightNumber: 101,
			simTime:      time.Date(2024, 1, 1, 9, 45, 0, 0, time.UTC), // this date in 2024 resolves to a Monday at 09:45
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N12347_101_0": {
					{
						IcaoOrigin:         "KJFK",
						IcaoDest:           "KLAX",
						DepatureHour:       10, // actual scheduled departure is 10:00 am
						DepartureMin:       0,
						DepartureDayOfWeek: 0,
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   0,
					},
				},
			},
			expectOrigin:  "KJFK",
			expectDest:    "KLAX",
			expectNoMatch: false,
		},
		{
			name:         "Match yesterday's flight arriving today",
			registration: "N54321",
			flightNumber: 202,
			simTime:      time.Date(2026, 1, 27, 6, 0, 0, 0, time.UTC), // this date in 2024 resolves to a Tuesday at 08:00
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N54321_202_0": { // Monday
					{
						IcaoOrigin:         "EGLL",
						IcaoDest:           "LFPG",
						DepatureHour:       22,
						DepartureMin:       0,
						DepartureDayOfWeek: 0, // Monday
						ArrivalHour:        7,
						ArrivalMin:         30,
						ArrivalDayOfWeek:   1, // Tuesday
					},
				},
			},
			expectOrigin:  "EGLL",
			expectDest:    "LFPG",
			expectNoMatch: false,
		},
		{
			name:          "No matching flight plan",
			registration:  "N99999",
			flightNumber:  999,
			simTime:       time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC),
			schedules:     map[string][]trafficglobal.ScheduledFlight{},
			expectNoMatch: true,
		},
		{
			name:         "Time is earlier than flight departure time",
			registration: "N11111",
			flightNumber: 111,
			simTime:      time.Date(2026, 1, 27, 5, 0, 0, 0, time.UTC), // 5am Tuesday, this is before 10:00 departure and outside max extended search window
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N11111_111_1": {
					{
						IcaoOrigin:         "KATL",
						IcaoDest:           "KMIA",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 1,
						ArrivalHour:        12,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   1,
					},
				},
			},
			expectNoMatch: true,
		},
		{
			name:         "Flight arrival time has passed",
			registration: "N22222",
			flightNumber: 222,
			simTime:      time.Date(2024, 1, 27, 18, 0, 0, 0, time.UTC), // 6pm Tuesday is after 13:00 arrival and outside max extended search window
			schedules: map[string][]trafficglobal.ScheduledFlight{
				"N22222_222_1": {
					{
						IcaoOrigin:         "KDFW",
						IcaoDest:           "KORD",
						DepatureHour:       10,
						DepartureMin:       0,
						DepartureDayOfWeek: 1,
						ArrivalHour:        13,
						ArrivalMin:         0,
						ArrivalDayOfWeek:   1,
					},
				},
			},
			expectNoMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atcService := New("config.yaml", tt.schedules)

			ac := &Aircraft{
				Registration: tt.registration,
				Flight: Flight{
					Number: tt.flightNumber,
				},
			}

			atcService.AddFlightPlan(ac, tt.simTime)

			if tt.expectNoMatch {
				if ac.Flight.Origin != "" || ac.Flight.Destination != "" {
					t.Errorf("expected no match, got Origin=%s Destination=%s", ac.Flight.Origin, ac.Flight.Destination)
				}
			} else {
				if ac.Flight.Origin != tt.expectOrigin {
					t.Errorf("expected Origin=%s, got %s", tt.expectOrigin, ac.Flight.Origin)
				}
				if ac.Flight.Destination != tt.expectDest {
					t.Errorf("expected Destination=%s, got %s", tt.expectDest, ac.Flight.Destination)
				}
			}
		})
	}
}
