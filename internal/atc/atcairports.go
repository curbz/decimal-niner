package atc

import "path/filepath"

func loadAirports(dir string, airportList map[string]bool, globalHolds map[string]*Hold) (map[string]*Airport, error) {
    airports := make(map[string]*Airport)

    for icao := range airportList {
        path := filepath.Join(dir, icao+".dat")

        // Parse runways for this airport
        rwyMap, err := ParseCIFP(path)
        if err != nil {
            continue
        }

        ap := &Airport{
            ICAO:    icao,
            Runways: make(map[string]Runway),
            Holds:   []*Hold{},
        }

        // Add runways
        for rwy, data := range rwyMap {
            ap.Runways[rwy] = data
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

func loadRunways(dir string, airports map[string]bool) (map[string]Runway, error) {
    out := make(map[string]Runway)

    for icao := range airports {
        path := filepath.Join(dir, icao+".dat")

        m, err := ParseCIFP(path)
        if err != nil {
            // Skip missing or unreadable files
            continue
        }

        for k, v := range m {
            out[k] = v
        }
    }

    return out, nil
}
