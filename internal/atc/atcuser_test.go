package atc

import "testing"

func TestNotifyUserStateChange(t *testing.T) {
	// shared controller used for matching
	ctrl := &Controller{
		Name:    "Test Tower",
		ICAO:    "TEST",
		RoleID:  2,
		Freqs:   []int{118050},
		Lat:     10.0,
		Lon:     20.0,
		IsPoint: true,
	}

	tests := []struct {
		name       string
		pos        Position
		tunedFreqs map[int]int
		tunedRoles map[int]int
		wantActive bool
		wantICAO   string
	}{
		{"match_present", Position{Lat: 10.0, Long: 20.0, Altitude: 1000}, map[int]int{1: 11805}, map[int]int{1: 2}, true, "TEST"},
		{"no_match", Position{Lat: 10.0, Long: 20.0, Altitude: 1000}, map[int]int{1: 12190}, map[int]int{1: 2}, false, ""},
		{"role_zero_converted", Position{Lat: 10.0, Long: 20.0, Altitude: 1000}, map[int]int{1: 11805}, map[int]int{1: 0}, true, "TEST"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{
				Controllers: []*Controller{ctrl},
			}

			s.NotifyUserStateChange(tt.pos, tt.tunedFreqs, tt.tunedRoles)

			if tt.wantActive {
				if s.UserState.ActiveFacilities == nil {
					t.Fatalf("expected ActiveFacilities to be non-nil")
				}
				c, ok := s.UserState.ActiveFacilities[1]
				if !ok || c == nil {
					t.Fatalf("expected active facility at index 1")
				}
				if s.UserState.NearestICAO != tt.wantICAO {
					t.Fatalf("NearestICAO = %q; want %q", s.UserState.NearestICAO, tt.wantICAO)
				}
			} else {
				if s.UserState.ActiveFacilities != nil {
					if _, ok := s.UserState.ActiveFacilities[1]; ok {
						t.Fatalf("expected no active facility for index 1")
					}
				}
				if s.UserState.NearestICAO != "" {
					t.Fatalf("expected NearestICAO to be empty; got %q", s.UserState.NearestICAO)
				}
			}
		})
	}
}
