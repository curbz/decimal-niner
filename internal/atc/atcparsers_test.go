package atc

import "testing"

func TestCleanFixName(t *testing.T) {
	tests := []struct {
		in  string
		out string
	}{
		{"RDU (COUNTRY)", "RDU"},
		{"KJFK", "KJFK"},
		{"SOME NAME VOR/DME", "SOME NAME"},
		{"ALPHA NDB", "ALPHA"},
		{"BRAVO VOR", "BRAVO"},
		{"CHARLIE FARLIE DME-ILS", "CHARLIE FARLIE"},
		{"DELTA DME", "DELTA"},
		{"NEW VORI VORTAC", "NEW VORI"},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := cleanFixName(tc.in)
			if got != tc.out {
				t.Errorf("cleanFixName(%q) = %q; want %q", tc.in, got, tc.out)
			}
		})
	}
}
