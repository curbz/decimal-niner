package atc

import "testing"

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
