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


func TestDistanceTrio_ConsistencyAndAccuracy(t *testing.T) {
	// Conversion ratios for checking mathematical alignment between functions
	const metersToFeet = 3.2808399
	const metersToNM = 0.0005399568

	tests := []struct {
		name     string
		lat1, lon1 float64
		lat2, lon2 float64
		// Real-world expected distance benchmarks in meters (WGS-84/Great-Circle approximations)
		expectedMeters float64 
		tolerancePct   float64 // Acceptable variance due to earth-spheroid approximations
	}{
		{
			name:           "EGLL to KJFK (Long Haul Aviation Route)",
			lat1:           51.4775, lon1: -0.4614, // London Heathrow
			lat2:           40.6398, lon2: -73.7789, // New York JFK
			expectedMeters: 5551000,                  // ~5,551 km
			tolerancePct:   0.005,                    // Allow within 0.5% due to varying radius definitions
		},
{
			name:           "EGKB Runway 03/21 Length (Medium-Short Layout)",
			lat1:           51.3236664, lon1: 0.0268658,
			lat2:           51.3382375, lon2: 0.0380562,
			expectedMeters: 1797.11, // Match the true spherical Haversine distance
			tolerancePct:   0.0001,  // Strict tolerance to prevent math regression
		},
		{
			name:           "Micro Distance (Airport Terminal Gate Taxiway Step)",
			lat1:           33.4342, lon1: -112.0115, 
			lat2:           33.4343, lon2: -112.0116,
			expectedMeters: 14.5,
			tolerancePct:   0.05, 
		},
		{
			name:           "Zero Distance (Stationary Aircraft Point)",
			lat1:           51.4775, lon1: -0.4614,
			lat2:           51.4775, lon2: -0.4614,
			expectedMeters: 0.0,
			tolerancePct:   0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 1. Calculate distances from the package APIs
			m := DistanceInMeters(tt.lat1, tt.lon1, tt.lat2, tt.lon2)
			ft := CalculateDistanceFeet(tt.lat1, tt.lon1, tt.lat2, tt.lon2)
			nm := DistNM(tt.lat1, tt.lon1, tt.lat2, tt.lon2)

			// 2. Perform Validation against benchmark values
			if tt.expectedMeters > 0 {
				diff := math.Abs(m - tt.expectedMeters)
				maxAllowedDiff := tt.expectedMeters * tt.tolerancePct
				if diff > maxAllowedDiff {
					t.Errorf("DistanceInMeters() = %.2f; expected roughly %.2f (outside tolerance)", m, tt.expectedMeters)
				}
			} else if m != 0.0 {
				t.Errorf("Expected exactly 0.0 for zero distance test, got %.2f", m)
			}

			// 3. STRIP TEST FOR TRIO DUPLICATION LOGIC
			// Since all three wrap greatCircleArc(), they should scale perfectly with your constants.
			expectedFtFromMeters := m * (EarthRadiusFt / EarthRadiusMeter)
			expectedNMFromMeters := m * (EarthRadiusNM / EarthRadiusMeter)

			if math.Abs(ft-expectedFtFromMeters) > 1e-4 {
				t.Errorf("Internal Mismatch: feet calculation (%.2f) does not scale identically with meters (%.2f)", ft, expectedFtFromMeters)
			}

			if math.Abs(nm-expectedNMFromMeters) > 1e-6 {
				t.Errorf("Internal Mismatch: NM calculation (%.4f) does not scale identically with meters (%.4f)", nm, expectedNMFromMeters)
			}
		})
	}
}
