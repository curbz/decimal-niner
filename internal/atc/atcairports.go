package atc

import (
	"errors"
	"io/fs"
	"log"
	"path/filepath"
)

type Airport struct {
	ICAO     string
	Name     string
	Lat      float64
	Lon      float64
	TransAlt int
	Region   string
	Runways  map[string]*Runway // keyed by "09L", "27R"
	Holds    []*Hold
}

type Runway struct {
	FAFalt       int    // Final approach fix altitude
	MAalt        int    // highest missed approach altitude
	MAHeading    int    // initial MA course (degrees)
	MAFix        string // only if HM leg exists
	BestApproach string // highest precision approach type
}

type Fix struct {
	Ident    string
	Region   string
	FullName string
	LatRad   float64
	LonRad   float64
}

func loadAirports(dir string, airports map[string]*Airport, airportList map[string]bool,
	airportHolds map[string][]*Hold, globalHolds map[string]*Hold) error {

	for icao := range airportList {

		// Parse airport CIFP data
		path := filepath.Join(dir, icao+".dat")
		rwyMap, err := ParseCIFP(path)
		var pathErr *fs.PathError
		if err != nil {
			if errors.As(err, &pathErr) {
				// if error is io/fs.PathError then prefix log message with WARN: otherwise report as error
				log.Println("WARN: CIFP file not found for airport", icao, ": ", err)
			} else {
				log.Println("error parsing CIFP file for airport", icao, ": ", err)
			}
			continue
		}

		ap, exists := airports[icao]
		if !exists {
			ap = &Airport{
				ICAO: icao,
			}
			airports[icao] = ap
		}

		ap.Runways = make(map[string]*Runway)
		ap.Holds = []*Hold{}

		// Add runways
		for rwy, data := range rwyMap {
			ap.Runways[rwy] = &data
		}

		// Add airport holds
		if hSlice, ok := airportHolds[icao]; ok {
			ap.Holds = append(ap.Holds, hSlice...)
		}

		// Ensure missed-approach holds are added - these can be defined as ENRT holds which is why they are not present in the
		// airportHolds map
		for _, rw := range ap.Runways {
			if rw.MAFix != "" {
				key := rw.MAFix + "_" + ap.Region
				if h, ok := globalHolds[key]; ok {
					// check hold not already present in array
					present := false
					if len(ap.Holds) > 0 {
						for _, aph := range ap.Holds {
							if aph.Ident == h.Ident && aph.Region == h.Region {
								// hold already present
								present = true
								break
							}
						}
					}
					if !present {
						ap.Holds = append(ap.Holds, h)
					}
				}
			}
		}
	}

	return nil
}
