package configui

import "testing"

func TestBrowserURL(t *testing.T) {
	tests := []struct {
		addr string
		want string
	}{
		{addr: "127.0.0.1:8097", want: "http://127.0.0.1:8097"},
		{addr: "localhost:8100", want: "http://localhost:8100"},
		{addr: "0.0.0.0:8097", want: "http://127.0.0.1:8097"},
		{addr: "[::1]:8097", want: "http://[::1]:8097"},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := BrowserURL(tt.addr)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("BrowserURL(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}
