package d9traffic

import (
	"math"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/pkg/geometry"
)

// getMinSpawnSeparationNM returns a minimum separation (in NM) to use when
// spawning aircraft based on size class heuristics.
func getMinSpawnSeparationNM(sizeClass string) float64 {
	switch sizeClass {
	case "E", "F":
		return 0.6 // Heavy/super
	case "C", "D":
		return 0.4 // Mainline jets
	default:
		return 0.25 // Props/light
	}
}

// ensureSafeSpawnPosition adjusts the provided spawn lat/lon outward if the
// point would be within minSepNM of any existing active aircraft. It moves the
// spawn point away from the nearest conflicting aircraft until the minimum
// separation is achieved or a maximum number of iterations is reached.
func (e *D9TrafficEngine) ensureSafeSpawnPosition(spawnLat *float64, spawnLon *float64, minSepNM float64) {
	if minSepNM <= 0 {
		return
	}

	const bufferNM = 0.1
	const maxIter = 20

	for i := 0; i < maxIter; i++ {
		nearestDist := math.MaxFloat64
		var nearest *atc.Aircraft

		for _, other := range e.ActiveAircraft {
			if other == nil {
				continue
			}
			d := geometry.DistNM(*spawnLat, *spawnLon, other.Flight.Position.Lat, other.Flight.Position.Long)
			if d < nearestDist {
				nearestDist = d
				nearest = other
			}
		}

		if nearest == nil || nearestDist >= minSepNM+bufferNM {
			return
		}

		// Move the spawn point away from the nearest aircraft by the shortfall
		shortfall := (minSepNM + bufferNM) - nearestDist
		// Bearing from the nearest aircraft to the spawn point (move along this to go away)
		bearing := geometry.CalculateBearing(nearest.Flight.Position.Lat, nearest.Flight.Position.Long, *spawnLat, *spawnLon)
		newLat, newLon := geometry.Project(*spawnLat, *spawnLon, bearing, shortfall)
		*spawnLat = newLat
		*spawnLon = newLon
	}
}
