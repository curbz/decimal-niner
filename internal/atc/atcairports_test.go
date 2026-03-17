package atc

import "testing"

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
