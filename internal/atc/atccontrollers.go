package atc

import (
	"bufio"
	"fmt"
	"math"

	"os"
	"strconv"
	"strings"

	"github.com/curbz/decimal-niner/internal/atc/flightphase"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
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

const RoleNone = -1

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
		"ctr":    6,
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
				cur.Freqs = append(cur.Freqs, normaliseFreq(fRaw))
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

func (s *Service) AssignController(ac *Aircraft) *Controller {

	// Identify AI's intended facility
	searchICAO := getAirportICAObyPhaseClass(ac)
	phaseFacility := atcFacilityByPhaseMap[flightphase.FlightPhase(ac.Flight.Phase.Current)]

	aiFac := s.locateController(
		ac.Registration,
		0, phaseFacility.roleId,
		ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude, searchICAO)

	// If we are in Cruise but looking for Center (6) and find nothing,
	// try looking for Departure (4) as a fallback before going to Unicom.
	if aiFac == nil && phaseFacility.roleId == 6 {
		aiFac = s.locateController(ac.Registration+"_CruiseFallback", 0, 4,
			ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude, searchICAO)
	}

	// Fallback: If no controller found (e.g. at a small grass strip),
	// look specifically for Unicom (Role 0)
	if aiFac == nil {
		aiFac = s.locateController(ac.Registration+"_Unicom", 0, 0,
			ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude, searchICAO)
	}

	// Final Global Fallback (Unicom anywhere nearby)
	// we check searchICAO isn't empty as we may have already performed the same search if the phase-based search returned no ICAO
	if aiFac == nil && searchICAO != "" {
		aiFac = s.locateController(ac.Registration+"_GlobalUnicom", 0, 0,
			ac.Flight.Position.Lat, ac.Flight.Position.Long, ac.Flight.Position.Altitude, "")
	}

	if aiFac == nil {
		util.LogWarnWithLabel(ac.Registration, "no ATC facility found")
		return nil
	}

	util.LogWithLabel(ac.Registration, "Controller found: %s %s Role: %s (%d)",
		aiFac.Name, aiFac.ICAO, roleNameMap[aiFac.RoleID], aiFac.RoleID)

	return aiFac
}

func (s *Service) locateController(label string, tFreq, tRole int, uLa, uLo, uAl float64, targetICAO string) *Controller {
	var bestMatch *Controller
	var bestPointMatch *Controller
	smallestArea := math.MaxFloat64

	// Adjust search limit: 100nm for frequency, 15nm for pure proximity
	searchLimit := 100.0
	if tFreq <= 0 {
		searchLimit = 15.0
	}
	closestPointDist := searchLimit

	util.LogWithLabel(label, "Searching controllers at lat %f, lng %f, elev %f. Target Role: %s (%d) Tuned Freq: %d Target ICAO: %s",
		uLa, uLo, uAl, roleNameMap[tRole], tRole, tFreq, targetICAO)

	// --- TIER 0: THE TARGET ICAO SHORTCUT ---
	if targetICAO != "" {
		ap, exists := s.Airports[targetICAO]
		if !exists {
			// airport not found, resort to proximity search
			util.LogWithLabel(label, "no airport found for target ICAO of %s - fallback to proximity search", targetICAO)
		} else {
			// distance sanity check - is this airport within 50nm?
			distToTarget := geometry.DistNM(uLa, uLo, ap.Lat, ap.Lon)
			if distToTarget < 50.0 {
				var backupMatch *Controller
				for _, c := range ap.Controllers {
					if c.ICAO == targetICAO && c.IsPoint {
						if tRole != RoleNone && c.RoleID == tRole {
							return c
						}
						if tRole == RoleNone {
							return c
						}
						if backupMatch == nil {
							backupMatch = c
						}
					}
				}
				if tRole == RoleNone && backupMatch != nil {
					return backupMatch
				}
			} else {
				util.LogWithLabel(label, "target ICAO %s is too far (%.2fnm) - fallback to proximity search", targetICAO, distToTarget)
			}
		}
	}

	// --- TIER 1: SCAN POINTS (Proximity + Frequency) ---
	for _, c := range s.Controllers {

		if !c.IsPoint || c.RoleID >= 7 {
			continue
		}

		// Vertical Gate: Ground/Tower/Delivery shouldn't be "reachable" at high altitude
		// Typically, these facilities are only tuned within the terminal environment.
		if tFreq > 0 && uAl > 10000 && (c.RoleID >= 1 && c.RoleID <= 3) {
			continue
		}

		dist := geometry.DistNM(uLa, uLo, c.Lat, c.Lon)
		if dist > searchLimit {
			continue
		}

		// Frequency Mode
		if tFreq > 0 {
			fMatch := false
			for _, f := range c.Freqs {
				if f/10 == tFreq/10 {
					fMatch = true
					break
				}
			}

			if fMatch {
				// Scenario A: User is looking for a specific role (e.g. Ground)
				if tRole != RoleNone && c.RoleID == tRole {
					if dist < closestPointDist {
						util.LogWithLabel(label, "  -> Freq/Role Match Found: %s %s (Role %d) at %.2fnm", c.Name, c.ICAO, c.RoleID, dist)
						closestPointDist = dist
						bestPointMatch = c
					}
				}

				// Scenario B: User is looking for ANY role (-1)
				if tRole == RoleNone {
					// Logic: Priority 1 is Distance. Priority 2 (Tie-break) is Role Importance.
					isSignificantImprovement := dist < (closestPointDist - 2.0)
					isSimilarDist := math.Abs(dist-closestPointDist) <= 2.0

					if bestPointMatch == nil || isSignificantImprovement || (isSimilarDist && c.RoleID > bestPointMatch.RoleID) {
						//util.LogWithLabel(label, "  -> Potential Freq Match Found: %s %s (Role %d) at %.2fnm", c.Name, c.ICAO, c.RoleID, dist)
						closestPointDist = dist
						bestPointMatch = c
					}
				}
			}
			continue
		}

		// Pure Proximity Mode (No Frequency)
		if dist < closestPointDist {
			if tRole != RoleNone && c.RoleID != tRole {
				continue
			}
			closestPointDist = dist
			bestPointMatch = c
		}
	}

	// High priority for airport facilities if low or frequency matched
	if (uAl < 5000 || tFreq > 0) && bestPointMatch != nil {
		return bestPointMatch
	}

	// --- TIER 2: SCAN POLYGONS (Center/Oceanic) ---
	for i := range s.Controllers {
		c := s.Controllers[i]
		if len(c.Airspaces) == 0 {
			continue
		}

		if tFreq > 0 {
			fMatch := false
			for _, f := range c.Freqs {
				if f/10 == tFreq/10 {
					fMatch = true
					break
				}
			}
			if !fMatch {
				continue
			}
		}

		for _, poly := range c.Airspaces {
			if uAl < poly.Floor || uAl > poly.Ceiling {
				continue
			}

			isInside := false
			if poly.MinLon <= poly.MaxLon {
				isInside = uLo >= poly.MinLon && uLo <= poly.MaxLon
			} else {
				isInside = uLo >= poly.MinLon || uLo <= poly.MaxLon
			}

			if isInside && uLa >= poly.MinLat && uLa <= poly.MaxLat {
				if geometry.IsPointInPolygon(uLa, uLo, poly.Points) {
					if poly.Area < smallestArea {
						if tRole == RoleNone || c.RoleID == tRole {
							smallestArea = poly.Area
							bestMatch = c
						}
					}
				}
			}
		}
	}

	if bestMatch != nil {
		return bestMatch
	}
	return bestPointMatch
}

func normaliseFreq(fRaw int) int {
	if fRaw == 0 {
		return 0
	}

	f := fRaw
	// X-Plane frequencies in apt.dat are often missing the trailing zero
	// or decimal precision. We want to scale everything to 1xx.xxx format
	// represented as an integer (e.g., 118050).

	for f < 100000 {
		f *= 10
	}

	// If the frequency ended up like 1180000 (too large),
	// we trim it back down.
	for f > 999999 {
		f /= 10
	}

	return f
}
