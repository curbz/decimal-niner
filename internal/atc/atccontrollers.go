package atc

import (
	"bufio"
	"fmt"
    
	"os"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/pkg/geometry"
)

type Controller struct {
	Name, ICAO string
	RoleID     int
	Freqs      []int
	Lat, Lon   float64
	IsPoint    bool
	IsRegion   bool
	Airspaces  []Airspace
}

type Airspace struct {
	Floor, Ceiling float64
	Points         [][2]float64
	Area           float64
	// Pre-calculated Bounding Box
	MinLat, MaxLat float64
	MinLon, MaxLon float64
}

type PhaseFacility struct {
	atcPhase string
	roleId   int
}

type Comms struct {
	Callsign       string
	Controller     *Controller
	NextController *Controller
	CruiseHandoff  int // flag to indicate to phrase generation that this is a handoff scenario and not just a routine position update
	CountryCode    string
}

type Handoff int

const (
	NoHandoff = iota
	HandoffExitSector
	HandoffEnterSector
)

func parseATCdatFiles(path string, isRegion bool, requiredICAOs map[string]bool) ([]*Controller, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open ATC data file %s: %w", path, err)
	}
	defer file.Close()

	roleMap := map[string]int{
		"del":    1,
		"gnd":    2,
		"twr":    3,
		"tracon": 4, // Approach/Departure
		"ctr":    6, // Standardized to 6 to match Center logic in parseApt function
	}

	scanner := bufio.NewScanner(file)
	var list []*Controller
	var cur *Controller
	var curPoly *Airspace
	var isRequired bool // Track if the current controller block should be kept

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		p := strings.Fields(line)

		switch strings.ToUpper(p[0]) {
		case "CONTROLLER":
			cur = &Controller{IsRegion: isRegion, IsPoint: false}
			isRequired = true // Default to true until we check the ICAO/Role
		case "NAME":
			if cur != nil {
				cur.Name = strings.Join(p[1:], " ")
			}
		case "FACILITY_ID", "ICAO":
			if cur != nil {
				cur.ICAO = p[1]
			}
		case "ROLE":
			if cur != nil {
				roleStr := strings.ToLower(p[1])
				cur.RoleID = roleMap[roleStr]

				// FILTER LOGIC:
				// If it's a local airport role (DEL, GND, TWR), check requiredICAOs.
				// If it's a wide-area role (TRACON, CTR), always keep it.
				if roleStr == "del" || roleStr == "gnd" || roleStr == "twr" {
					if _, found := requiredICAOs[cur.ICAO]; !found {
						isRequired = false
					}
				}
			}
		case "FREQ", "CHAN":
			if cur != nil && isRequired {
				fRaw, _ := strconv.Atoi(p[1])
				cur.Freqs = append(cur.Freqs, normalizeFreq(fRaw))
			}
		case "AIRSPACE_POLYGON_BEGIN":
			if !isRequired {
				continue
			}
			f, c := -99999.0, 99999.0
			if len(p) >= 3 {
				f, _ = strconv.ParseFloat(p[1], 64)
				c, _ = strconv.ParseFloat(p[2], 64)
			}
			curPoly = &Airspace{Floor: f, Ceiling: c}
		case "POINT":
			if !isRequired {
				continue
			}
			la, _ := strconv.ParseFloat(p[1], 64)
			lo, _ := strconv.ParseFloat(p[2], 64)
			if curPoly != nil {
				curPoly.Points = append(curPoly.Points, [2]float64{la, lo})
			}
			if cur != nil && cur.Lat == 0 {
				cur.Lat, cur.Lon = la, lo
			}
		case "AIRSPACE_POLYGON_END":
			if cur != nil && curPoly != nil {
				curPoly.Area = geometry.CalculateRoughArea(curPoly.Points)

				// 1. Initialize bounds
				minLa, maxLa := 90.0, -90.0
				minLo, maxLo := 180.0, -180.0

				// 2. Standard Lat bounds
				for _, p := range curPoly.Points {
					if p[0] < minLa {
						minLa = p[0]
					}
					if p[0] > maxLa {
						maxLa = p[0]
					}
				}

				// 3. Smart Longitude bounds (detect wrap-around)
				// Find the "gap" in longitude to determine if we cross the dateline
				actualMinLo, actualMaxLo := 180.0, -180.0
				hasEast := false
				hasWest := false

				for _, p := range curPoly.Points {
					lon := p[1]
					if lon > 0 {
						hasEast = true
					}
					if lon < 0 {
						hasWest = true
					}
					if lon < actualMinLo {
						actualMinLo = lon
					}
					if lon > actualMaxLo {
						actualMaxLo = lon
					}
				}

				// If a polygon has points in both East and West AND spans a huge distance,
				// it's a dateline crosser (like Anchorage)
				if hasEast && hasWest && (actualMaxLo-actualMinLo > 180) {
					// Anchorage Case: Min is the smallest positive, Max is the largest negative
					// effectively "wrapping around" the back of the map.
					minLo, maxLo = 180.0, -180.0
					for _, p := range curPoly.Points {
						if p[1] > 0 && p[1] < minLo {
							minLo = p[1]
						} // Smallest East (e.g. 165)
						if p[1] < 0 && p[1] > maxLo {
							maxLo = p[1]
						} // Largest West (e.g. -140)
					}
				} else {
					// Standard case
					minLo, maxLo = actualMinLo, actualMaxLo
				}

				curPoly.MinLat, curPoly.MaxLat = minLa, maxLa
				curPoly.MinLon, curPoly.MaxLon = minLo, maxLo

				cur.Airspaces = append(cur.Airspaces, *curPoly)
			}
			curPoly = nil
		case "CONTROLLER_END":
			if cur != nil && isRequired {
				list = append(list, cur)
			}
			cur = nil
			isRequired = false
		}
	}
	return list, nil
}

