package atc

import (
	"fmt"
	"testing"
)


func TestPerformSearch(t *testing.T) {

	atcService := New("../../config.yaml")

	tests := []struct {
		label string
		f, r  int
		la, lo, al float64
	}{
		{"Heathrow Tower (Freq Match)", 118505, 3, 51.4706, -0.4522, 1000.0},
		{"London Center (Polygon Match)", 121520, 5, 51.5, -0.1, 20000.0}, 
		{"Shoreham Ground (Proximity Match)", 0, 2, 50.835, -0.297, 50.0},
	}

	for _, t := range tests {
		m := atcService.PerformSearch(t.label, t.f, t.r, t.la, t.lo, t.al)
		if m != nil {
			fmt.Printf("FINAL RESULT: %s (%s)\n\n", m.Name, m.ICAO)
		} else {
			fmt.Printf("FINAL RESULT: NO MATCH\n\n")
		}
	}

}
