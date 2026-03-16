package atc

import (
	"github.com/curbz/decimal-niner/internal/trafficglobal"
)

type Weather struct {
	Wind       Wind
	Baro       Baro
	Temp       float64
	Vis        float64
	Humidity   float64
	MagVar     float64
	Turbulence float64 // magnitude 0-10
}

type Wind struct {
	Direction float64 // degrees
	Speed     float64 // m/s
	Shear     float64 // m/s
}

type Baro struct {
	Flight   float64
	Sealevel float64
}

func (s *Service) GetWeatherState() *Weather {
	return s.Weather
}

func getTransitionLevel(transitionAlt int, currBaroPascals float64) int {
	// Standard pressure in inches of mercury
	const standardPressure = 101325.0 // Pascals
	
	// If pressure is standard or higher, TL is usually TA + 1000ft
	if currBaroPascals >= standardPressure {
		return (transitionAlt / 100) + 10 // e.g., 6000ft -> FL70
	}
	
	// If pressure is low, we need more space, so we add an extra level
	return (transitionAlt / 100) + 20 // e.g., 6000ft -> FL80
}

// scaleAltitude rounds the altitude and scales to either feet or flight level. The returned bool value
// is true when the scale is flight levels and false when the returned value is an altitude in feet
func scaleAltitude(rawAlt float64, transitionLevel int, phase Phase) (int, bool) {

	var roundedAlt int
	alt := int(rawAlt)

	// Contextual Rounding Logic
	switch phase.Current {
	case trafficglobal.Final.Index(), trafficglobal.Approach.Index():
		// Nearest 100ft for precision during landing (e.g., 2,412 -> 2,400)
		roundedAlt = ((alt + 50) / 100) * 100
	default:
		// Standard IFR rounding to nearest 1,000ft (e.g., 33,240 -> 33,000)
		roundedAlt = ((alt + 500) / 1000) * 1000
	}

	// Flight Level Logic (At or above Transition Altitude)
	if roundedAlt >= (transitionLevel * 100) || roundedAlt >= 18000 {
		fl := roundedAlt / 100

		// Ensure cruise flight levels are multiples of 10 (e.g., 330)
		if phase.Current == trafficglobal.Cruise.Index() {
			fl = (fl / 10) * 10
		}

		// Returns "flight level 330"
		return fl, true
	}

	return roundedAlt, false
}

