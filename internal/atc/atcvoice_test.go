package atc

import (
	"testing"

	"github.com/curbz/decimal-niner/internal/trafficglobal"
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
			phaseCurrent:    trafficglobal.Approach.Index(),
			wantVal:         2400,
			wantIsFL:        false,
		},
		{
			name:            "default rounds to thousands and remains feet below transition",
			rawAlt:          3240,
			transitionLevel: 200, // 20000
			phaseCurrent:    trafficglobal.Unknown.Index(),
			wantVal:         3000,
			wantIsFL:        false,
		},
		{
			name:            "default rounds to thousands and becomes flight level above transition",
			rawAlt:          33240,
			transitionLevel: 200, // 20000
			phaseCurrent:    trafficglobal.Unknown.Index(),
			wantVal:         330, // 33000 -> /100 = 330
			wantIsFL:        true,
		},
		{
			name:            ">=18000 becomes flight level even if transition is higher",
			rawAlt:          18001,
			transitionLevel: 500, // 50000 so threshold not reached, but 18000 rule applies
			phaseCurrent:    trafficglobal.Unknown.Index(),
			wantVal:         180, // 18000 -> /100
			wantIsFL:        true,
		},
		{
			name:            "cruise flight level is a multiple of 10",
			rawAlt:          33499,
			transitionLevel: 200,
			phaseCurrent:    trafficglobal.Cruise.Index(),
			// rounded -> ((33499+500)/1000)*1000 = 33000 -> fl 330 (already multiple of 10)
			wantVal:  330,
			wantIsFL: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := Phase{Current: tt.phaseCurrent}
			gotVal, gotFL := scaleAltitude(tt.rawAlt, tt.transitionLevel, ph)
			if gotVal != tt.wantVal || gotFL != tt.wantIsFL {
				t.Fatalf("%s: scaleAltitude(%v,%d,phase) = (%d,%v); want (%d,%v)", tt.name, tt.rawAlt, tt.transitionLevel, gotVal, gotFL, tt.wantVal, tt.wantIsFL)
			}
		})
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
			phaseCurrent:    trafficglobal.Unknown.Index(),
			want:            "flight level 330",
		},
		{
			name:            "feet clean thousand",
			rawAlt:          5000,
			transitionLevel: 200,
			phaseCurrent:    trafficglobal.Unknown.Index(),
			want:            "5 thousand",
		},
		{
			name:            "approach split hundreds",
			rawAlt:          2412,
			transitionLevel: 200,
			phaseCurrent:    trafficglobal.Approach.Index(),
			want:            "2 thousand 4 hundred",
		},
		{
			name:            "below transition remains feet",
			rawAlt:          3240,
			transitionLevel: 400, // higher transition so stays feet
			phaseCurrent:    trafficglobal.Unknown.Index(),
			want:            "3 thousand",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ph := Phase{Current: tt.phaseCurrent}
			got := formatAltitude(tt.rawAlt, tt.transitionLevel, ph)
			if got != tt.want {
				t.Fatalf("%s: formatAltitude(...) = %q; want %q", tt.name, got, tt.want)
			}
		})
	}
}
