package atc

import (
	"fmt"
	"os"
	"testing"

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
		label string
		f, r  int
		la, lo, al float64
		icao string
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
