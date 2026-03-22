package atc

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

