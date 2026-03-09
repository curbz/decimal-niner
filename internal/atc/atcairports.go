package atc

import (
	"errors"
	"io/fs"
	"log"
	"path/filepath"
)

type Airport struct { 
	ICAO string 
	Name string
	Lat float64
	Lon float64
	TransAlt int
	Runways map[string]*Runway // keyed by "09L", "27R" 
	Holds []*Hold // both MA holds and arrival-stack holds 
}

type Runway struct {
    FAFalt       int    // Final approach fix altitude
    MAalt        int    // highest missed approach altitude
    MAHeading    int    // initial MA course (degrees)
    MAFix        string // only if HM leg exists
    BestApproach string // highest precision approach type
}

type Fix struct {
    Ident  string
    Region string
	FullName string
    LatRad float64
    LonRad float64
}

func loadAirports(dir string, airportList map[string]bool, globalHolds map[string]*Hold) (map[string]*Airport, error) {
    airports := make(map[string]*Airport)

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

        ap := &Airport{
            ICAO:    icao,
            Runways: make(map[string]*Runway),
            Holds:   []*Hold{},
        }

        // Add runways
        for rwy, data := range rwyMap {
            ap.Runways[rwy] = &data
        }

        // Add missed-approach holds if present in global holds
        for _, rw := range ap.Runways {
            if rw.MAFix != "" {
                if h, ok := globalHolds[rw.MAFix]; ok {
                    ap.Holds = append(ap.Holds, h)
                }
            }
        }
        airports[icao] = ap
    }

    return airports, nil
}
