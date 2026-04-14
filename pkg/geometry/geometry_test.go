package geometry

import (
	"math"
	"testing"
)

func TestProject(t *testing.T) {
    tests := []struct {
        name       string
        startLat   float64
        startLon   float64
        heading    float64
        distance   float64
        wantLat    float64
        wantLon    float64
        tolerance  float64
    }{
        {
            name:      "Project North from Heathrow",
            startLat:  51.4700,
            startLon:  -0.4543,
            heading:   0.0,
            distance:  10.0, // 10 NM
            wantLat:   51.6366,
            wantLon:   -0.4543,
            tolerance: 0.001,
        },
        {
            name:      "Project East from Heathrow",
            startLat:  51.4700,
            startLon:  -0.4543,
            heading:   90.0,
            distance:  10.0,
            wantLat:   51.4697, // Slightly south due to GC path
            wantLon:   -0.1873,
            tolerance: 0.001,
        },
        {
            name:      "Project West (Heading 270)",
            startLat:  51.4700,
            startLon:  -0.4543,
            heading:   270.0,
            distance:  5.0,
            wantLat:   51.4699,
            wantLon:   -0.5878,
            tolerance: 0.001,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            gotLat, gotLon := Project(tt.startLat, tt.startLon, tt.heading, tt.distance)
            if math.Abs(gotLat-tt.wantLat) > tt.tolerance || math.Abs(gotLon-tt.wantLon) > tt.tolerance {
                t.Errorf("Project() = %v, %v, want %v, %v", gotLat, gotLon, tt.wantLat, tt.wantLon)
            }
        })
    }
}

