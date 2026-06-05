package atc

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/curbz/decimal-niner/internal/constants"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"github.com/curbz/decimal-niner/pkg/util"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

type Airport struct {
	ICAO            string
	Name            string
	Lat             float64
	Lon             float64
	Elevation       float64 // feet
	TransAlt        int
	Region          string
	Runways         map[string]*Runway // keyed by "09L", "27R"
	RunwayUsageData UsageMap           // temporary field used whilst loading data
	Holds           []*Hold
	Controllers     []*Controller
	Parking         map[string]*ParkingSpot // keyed by ParkingSpot.Name
	HubWeights      map[string]float64      // Airline ICAO -> Strength (0.0 to 1.0)
	ClassCounts     map[string]int          // "E": 20, "C": 100 (Total gates by size)
}

type Runway struct {
	Name                     string  // e.g., "09L"
	Lat, Lon                 float64 // The coordinates of the threshold
	EndLat, EndLon           float64 // The coordinates of the opposite threshold (used for runway access logic)
	Heading                  float64 // The magnetic or true heading of the runway
	Length                   float64 // Length in meters
	Width                    float64 // Width in meters
	ThresholdElevation       float64 // feet
	FAFalt                   int     // Final approach fix altitude
	MAalt                    int     // highest missed approach altitude
	MAHeading                int     // initial MA course (degrees)
	MAFix                    string
	HighestPrecisionApproach string // highest precision approach type
	SIDs                     []*Procedure
	STARs                    []*Procedure
	DepartureAccess          map[string]*AccessPoint // Key: "A13", Value: AccessPoint{Coord, "Foxtrot"}
	ArrivalAccess            map[string]*AccessPoint
}

type Procedure struct {
	Name  string
	Type  int // 0 = SID, 1 = STAR
	Entry *ProcedureFix
	Exit  *ProcedureFix
}

type ParkingSpot struct {
	Name         string
	AirlineCodes string // space-separated list of 3 letter airline codes that can use this spot (e.g., "baw klm"), or empty for any
	Lat, Lon     float64
	Heading      float64
	Type         string // Gate, Tie-down, Hangar
	WidthClass   string // A, B, C, D, E, F (ICAO standard)
	SizeType     string // airline / general_aviation / military
	IsOccupied   bool
	TaxiwayName  string // The taxiway this spot feeds into (e.g., "Foxtrot")
}

// AccessPoint is used to define pathways for runways and parking spots
type AccessPoint struct {
	Name        string
	Coord       Coordinate
	TaxiwayName string // e.g., "Alpha" (The main taxiway this holding point feeds into)
	Dist        float64
	Bearing     float64
	IsHighSpeed bool // true when designated as a high speed (RTE) exit for an arrival runway
	IsNearEnd   bool // true when access point is close to the end of an arrival runway
}

type pendingProc struct {
	Name       string
	Type       int    // 0 = SID, 1 = STAR
	RunwayName string // e.g., "09L" or "ALL"
	Legs       []ProcedureFix
}

// RunwayID -> TaxiwayName -> UsageType
type UsageMap map[string]map[string]string

type RawEdge struct {
	NodeA    int
	NodeB    int
	TaxiName string
}

type aptPoint struct {
	Lat, Lon float64
}

type NamedNode struct {
	Lat, Lon   float64
	TaxiName   string
	Importance int
}

const (
	DEPARTURE_CONTEXT = 0
	ARRIVAL_CONTEXT   = 1
)

func (s *Service) GetClosestAirport(lat, lon, withinRangeNm float64) string {
	var closestICAO string
	for icao, coords := range s.Airports {

		dist := geometry.DistNM(lat, lon, coords.Lat, coords.Lon)

		if dist < withinRangeNm {
			withinRangeNm = dist
			closestICAO = icao
		}
	}

	return closestICAO
}

// GetAirportRunwayByICAO returns the Runway instance for the given airport ICAO and runway name and nil if not found
func (s *Service) GetAirportRunwayByICAO(icao, rwy string) *Runway {
	var r *Runway
	if icao != "" && rwy != "" {
		ap, found := s.Airports[icao]
		if found {
			r, _ = ap.Runways[rwy]
		}
	}
	return r
}

// GetAirportRunway returns the Runway instance for the given airport and runway name and nil if not found
func (s *Service) GetAirportRunway(airport *Airport, rwy string) *Runway {
	var r *Runway
	if airport != nil && rwy != "" {
		r, _ = airport.Runways[rwy]
	}
	return r
}

func (s *Service) GetAirportByICAO(icao string) *Airport {
	ap, exists := s.Airports[icao]
	if !exists {
		return nil
	}
	return ap
}

func (s *Service) GetParkingSpotByName(icao, name string) *ParkingSpot {
	ap := s.GetAirportByICAO(icao)
	if ap == nil {
		return nil
	}
	spot, exists := ap.Parking[name]
	if !exists {
		return nil
	}
	return spot
}

func (s *Service) AssignSID(ac *Aircraft, airport *Airport, depRwy *Runway) {

	if depRwy == nil {
		util.LogErrWithLabel(ac.Registration, "unable to assign SID as no runway provided (nil)")
		return
	}

	//SID assignment
	destAirport := s.GetAirportByICAO(ac.Flight.Destination)
	if destAirport == nil {
		util.LogWarnWithLabel(ac.Registration, "destination airport %s not found - unable to assign SID", ac.Flight.Destination)
		return
	}

	// Calculate the bearing from the airport to the destination
	bearingToTarget := geometry.CalculateBearing(airport.Lat, airport.Lon, destAirport.Lat, destAirport.Lon)

	var bestSID *Procedure
	minDiff := 360.0

	for i := range depRwy.SIDs {
		sid := depRwy.SIDs[i]
		// For a SID, we look at the EXIT fix (where the plane enters the enroute structure)
		sidBearing := geometry.CalculateBearing(airport.Lat, airport.Lon, sid.Exit.Fix.Lat, sid.Exit.Fix.Lon)

		diff := math.Abs(geometry.BearingDiff(bearingToTarget, sidBearing))
		if diff < minDiff {
			minDiff = diff
			bestSID = sid
		}
	}

	if bestSID != nil {
		ac.Flight.AssignedSID = bestSID
		util.LogWithLabel(ac.Registration, "assigned %s SID", bestSID.Name)
		return
	}

}

func (s *Service) AssignSTAR(ac *Aircraft, airport *Airport, arrRwy *Runway) {

	if arrRwy == nil {
		util.LogErrWithLabel(ac.Registration, "unable to assign STAR as no runway provided (nil)")
		return
	}

	// 30% probability of STAR assignment to allow for vectoring as alternative
	if rand.Float32() < constants.STARProbabilityFactor && len(arrRwy.STARs) > 0 {
		var bestSTAR *Procedure
		minDiff := 360.0

		origAirport := s.GetAirportByICAO(ac.Flight.Origin)
		if origAirport == nil {
			util.LogWarnWithLabel(ac.Registration, "origin airport %s not found - unable to assign STAR", ac.Flight.Origin)
			ac.Flight.Vectoring = true
			util.LogWithLabel(ac.Registration, "no arrival procedure assigned - aircraft will be vectored to runway by ATC")
			return
		}

		// Calculate the bearing from the origin to the destination
		bearingToTarget := geometry.CalculateBearing(origAirport.Lat, origAirport.Lon, airport.Lat, airport.Lon)

		for i := range arrRwy.STARs {
			star := arrRwy.STARs[i]
			// For a STAR, we look at the ENTRY fix (where the plane starts the arrival)
			starBearing := geometry.CalculateBearing(airport.Lat, airport.Lon, star.Entry.Fix.Lat, star.Entry.Fix.Lon)

			diff := math.Abs(geometry.BearingDiff(bearingToTarget, starBearing))
			if diff < minDiff {
				minDiff = diff
				bestSTAR = star
			}
		}

		if bestSTAR != nil {
			ac.Flight.AssignedSTAR = bestSTAR
			util.LogWithLabel(ac.Registration, "assigned STAR %s", bestSTAR.Name)
			return
		}
	} else {
		ac.Flight.Vectoring = true
		util.LogWithLabel(ac.Registration, "no arrival procedure assigned - aircraft will be vectored to runway by ATC")
		return
	}

}

// assignRunwayAccessPoint assigns the runway access or exit point depending on whether the arrOrDep flag
// is set to arrival (0) or departure (1)
func (s *Service) AssignRunwayAccessPoint(ac *Aircraft, ap *Airport, arrOrDep int) {

	minDistToGate := math.MaxFloat64
	var selected *AccessPoint
	spot := ac.Flight.AssignedParkingSpot

	rwy := ac.Flight.AssignedRunway
	if rwy == nil {
		var exists bool
		rwy, exists = ap.Runways[ac.Flight.AssignedRunwayName]
		if !exists {
			util.LogErrWithLabel(ac.Registration, "unable to assign runway access - runway name '%s' not found at %s",
				ac.Flight.AssignedRunwayName, ap.ICAO)
			return
		}
		ac.Flight.AssignedRunway = rwy
	}

	var accessMap map[string]*AccessPoint
	if arrOrDep == ARRIVAL_CONTEXT {
		accessMap = rwy.ArrivalAccess
		//TODO: decide on if we want logic to consider IsHighSpeed, aircraft size, IsNearEnd etc. for arrivals
	} else {
		accessMap = rwy.DepartureAccess
	}

	for _, access := range accessMap {
		// Which of these qualified entries is closest to our PARKED position?
		dist := geometry.DistNM(spot.Lat, spot.Lon, access.Coord.Lat, access.Coord.Lon)
		if dist < minDistToGate {
			minDistToGate = dist
			selected = access
		}
	}

	if arrOrDep == ARRIVAL_CONTEXT {
		ac.Flight.ArrivalAccess = selected
	} else {
		ac.Flight.DepartureAccess = selected
	}

}

func loadAirports(dir string, airports map[string]*Airport, requiredAirports map[string]bool,
	airportHolds map[string][]*Hold, allHolds map[string]*Hold, allFixes map[string]*Fix) error {

	for icao := range requiredAirports {

		ap, exists := airports[icao]
		if !exists {
			ap = &Airport{
				ICAO: icao,
			}
			airports[icao] = ap
		}

		if ap.Runways == nil {
			ap.Runways = make(map[string]*Runway)
		}

		// Parse airport CIFP data for runway, approach and fixes data
		path := filepath.Join(dir, icao+".dat")
		err := parseCIFP(path, allFixes, ap)
		var pathErr *fs.PathError
		if err != nil {
			if errors.As(err, &pathErr) {
				// if error is io/fs.PathError then prefix log message with WARN: otherwise report as error
				logger.Log.Warn("CIFP file not found for airport ", icao, ": ", err)
			} else {
				logger.Log.Error("error parsing CIFP file for airport ", icao, ": ", err)
			}
			continue
		}

		ap.Holds = []*Hold{}
		// Add airport holds
		if hSlice, ok := airportHolds[icao]; ok {
			ap.Holds = append(ap.Holds, hSlice...)
		}

		// Ensure missed-approach holds are added - these can be defined as ENRT holds which is why they are not present in the
		// airportHolds map
		for _, rw := range ap.Runways {
			if rw.MAFix != "" {
				key := rw.MAFix + "_" + ap.Region
				if h, ok := allHolds[key]; ok {
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

// parseApt processes the X-Plane apt.dat file with deep fallback logic for missing coordinates.
func parseApt(path string, requiredAirports map[string]bool) ([]*Controller, map[string]*Airport, error) {

	var allcontrollers, apcontrollers []*Controller
	airports := make(map[string]*Airport)

	var (
		nodeBuffer = make(map[int]Coordinate) // NodeID -> Lat/Lon
		edgeBuffer = []RawEdge{}              // List of all segments for the current airport
	)

	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open airports data file %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var curAirport *Airport
	var curICAO, curName, region string
	var curLat, curLon, curElev float64
	var transAlt int
	var isRequiredAirport bool
	var isRequiredController bool
	var batchStartIdx int
	var airportPoints []aptPoint
	var curParking *ParkingSpot // Temporary pointer to the spot being built
	var curTaxiNames []string
	canClearTaxiNames := false

	roleMap := map[string]int{
		"1050": 7, // Information (Weather)
		"1051": 0, // Defaulting 1051 to Role 0 (Unicom/Radio)
		"1052": 1, // Delivery
		"1053": 2, // Ground
		"1054": 3, // Tower
		"1056": 4, // Departure
		"1055": 5, // Approach
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		p := strings.Fields(line)
		if len(p) < 2 {
			continue
		}
		code := p[0]

		// 1. HEADER RECORD (New Airport Start)
		if code == "1" || code == "16" || code == "17" {
			isRequiredController = (code == "1")

			if curAirport != nil {
				finaliseAirport(curAirport, curLat, curLon, airportPoints, apcontrollers, curElev, nodeBuffer, edgeBuffer)
			}

			if len(apcontrollers) > 0 {
				allcontrollers = append(allcontrollers, apcontrollers...)
				// clear apcontrollers for next airport
				apcontrollers = []*Controller{}
			}

			if len(p) >= 5 {
				curICAO = p[4]
				curName = cleanAirportName(strings.Join(p[5:], " "))
				curLat = 0.0
				curLon = 0.0
				transAlt = 0
				region = ""
				curElev, err = strconv.ParseFloat(p[1], 0)
				if err != nil {
					logger.Log.Warnf("unable to get airport elevation for %s: %v", curICAO, err)
				}
				airportPoints = []aptPoint{}

				_, isRequiredAirport = requiredAirports[curICAO]
				if strings.HasPrefix(curICAO, "[H]") || strings.HasPrefix(curICAO, "[X]") {
					isRequiredAirport = false
				}

				if isRequiredAirport {
					// start building new airport - clear all buffers and temp data
					nodeBuffer = make(map[int]Coordinate) // Reset
					edgeBuffer = edgeBuffer[:0]           // Clear
					curTaxiNames = []string{}
					curAirport = &Airport{
						ICAO:        curICAO,
						Name:        curName,
						Controllers: []*Controller{},
						Runways:     make(map[string]*Runway),
					}
					airports[curICAO] = curAirport
				} else {
					curAirport = nil
				}
			}
			continue
		}

		// 1300: PARKING LOCATION
		// 1300 51.469151 -0.446896 -92.6 gate heavy|jets 218L
		if curAirport != nil && code == "1300" {
			if len(p) >= 7 {
				lat, _ := strconv.ParseFloat(p[1], 64)
				lon, _ := strconv.ParseFloat(p[2], 64)
				hdg, _ := strconv.ParseFloat(p[3], 64)

				curParking = &ParkingSpot{
					Lat:     lat,
					Lon:     lon,
					Heading: hdg,
					Type:    p[4],
					Name:    strings.Join(p[6:], " "),
				}
				// We don't add it to curAirport.Parking yet; we wait for metadata (1301)
			}
			continue
		}

		// 1301: PARKING METADATA (Follows a 1300)
		// 1301 C airline baw afr klm dlh vir sas aza ibe sva ber ryr vlg ezy
		if curAirport != nil && code == "1301" && curParking != nil {
			// Initialize Parking map if not yet done
			if curAirport.Parking == nil {
				curAirport.Parking = make(map[string]*ParkingSpot)
			}
			airlineCodes := ""
			if len(p) >= 3 {
				if p[2] != "airline" { // airline / general_aviation / military
					// d9traffic is not interested in non-airline parking
					curParking = nil
					continue
				}
				curParking.WidthClass = p[1] // Size class (e.g., "D")
				curParking.SizeType = p[2]
				if len(p) >= 4 {
					airlineCodes = strings.Join(p[3:], " ")
				}
			}
			curParking.AirlineCodes = airlineCodes
			curAirport.Parking[curParking.Name] = curParking
			curParking = nil // Reset for next spot
			continue
		}

		// 2. GEOGRAPHY & METADATA (Universal Parsing) - this is need for controller data regardless of whether airport is required or not, as controllers are parsed in same loop and may be present at non-required airports
		if code == "1302" && len(p) == 3 {
			switch p[1] {
			case "datum_lat":
				curLat, _ = strconv.ParseFloat(p[2], 64)
			case "datum_lon":
				curLon, _ = strconv.ParseFloat(p[2], 64)
			case "transition_alt":
				transAlt, _ = strconv.Atoi(p[2])
			case "region_code":
				region = p[2]
			}
			if curAirport != nil {
				curAirport.TransAlt, curAirport.Region = transAlt, region
			}
			continue
		}

		// Runway records (100 = Asphalt/Concrete, 101 = Water, 102 = Helipad)
		if code == "100" || code == "101" || code == "102" {
			fields := strings.Fields(line)
			if code == "102" && len(fields) >= 4 {
				// Helipad format: 102 <designator> <lat> <lon> ...
				hLat, _ := strconv.ParseFloat(fields[2], 64)
				hLon, _ := strconv.ParseFloat(fields[3], 64)
				if hLat != 0 && hLon != 0 {
					airportPoints = append(airportPoints, aptPoint{hLat, hLon})
				}
			} else if (code == "100" || code == "101") && len(fields) >= 20 {
				width, _ := strconv.ParseFloat(fields[1], 64)
				lat1, _ := strconv.ParseFloat(fields[9], 64)
				lon1, _ := strconv.ParseFloat(fields[10], 64)
				lat2, _ := strconv.ParseFloat(fields[18], 64)
				lon2, _ := strconv.ParseFloat(fields[19], 64)

				// DYNAMIC LENGTH MATH: Calculate physical length from coordinates
				calculatedLength := 0.0
				if lat1 != 0 && lat2 != 0 {
					calculatedLength = geometry.DistanceInMeters(lat1, lon1, lat2, lon2)
				}

				if curAirport != nil {
					id1 := fields[8]
					rwy1 := getOrCreateRunway(curAirport, id1)
					rwy1.Lat = lat1
					rwy1.Lon = lon1
					rwy1.EndLat = lat2
					rwy1.EndLon = lon2
					rwy1.Width = width
					rwy1.Length = calculatedLength

					id2 := fields[17]
					if id2 != "" && id2 != "xxx" && id2 != "nil" {
						rwy2 := getOrCreateRunway(curAirport, id2)
						rwy2.Lat = lat2
						rwy2.Lon = lon2
						rwy2.EndLat = lat1
						rwy2.EndLon = lon1
						rwy2.Width = width
						rwy2.Length = calculatedLength
					}
				}
				if lat1 != 0 && lat2 != 0 {
					airportPoints = append(airportPoints, aptPoint{lat1, lon1}, aptPoint{lat2, lon2})
				}
			}
			continue
		}

		// 3. FREQUENCY RECORDS
		if isRequiredController {
			if rID, ok := roleMap[code]; ok {
				isEnroute := rID >= 4

				//THE AIRSPACE-AWARE GATEKEEPER
				if !isRequiredAirport {
					if len(airportPoints) == 0 {
						// True standalone TRACONs/Centers are strictly Approach (5) or Departure (4).
						// If it's anything else (like Role 7 Information/ATIS) without a runway, KILL IT.
						if rID != 4 && rID != 5 {
							continue
						}
					}
				}

				if isRequiredAirport || isEnroute {
					fRaw, _ := strconv.Atoi(p[1])
					fNorm := normaliseFreq(fRaw)
					batchStartIdx = len(apcontrollers)

					// DYNAMIC POSITION CORRECTION
					// If 1302 datum was missing, calculate a fallback coordinate using
					// the runway physical layout points collected so far before generating the controller.
					targetLat := curLat
					targetLon := curLon
					if targetLat == 0 && targetLon == 0 && len(airportPoints) > 0 {
						var sumLat, sumLon float64
						for _, pt := range airportPoints {
							sumLat += pt.Lat
							sumLon += pt.Lon
						}
						targetLat = sumLat / float64(len(airportPoints))
						targetLon = sumLon / float64(len(airportPoints))
					}

					roles := []int{rID}
					if code == "1051" || code == "1054" {
						roles = []int{rID, 1, 2}
					}

					for _, r := range roles {
						// X-Plane synthetic objects typically use 5-7 character procedural codes
						// starting with X, or containing internal runway/localizer tags.
						if strings.HasPrefix(curICAO, "XLIL") || strings.HasPrefix(curICAO, "X") && len(curICAO) > 4 {
							continue // Ignore simulated structural helper entries
						}
						c := &Controller{
							Name:    curName,
							ICAO:    curICAO,
							RoleID:  r,
							Freqs:   []int{fNorm},
							IsPoint: true,
							Lat:     targetLat,
							Lon:     targetLon,
						}
						apcontrollers = append(apcontrollers, c)
						if curAirport != nil {
							curAirport.Controllers = append(curAirport.Controllers, c)
						}
					}
				}
				continue
			}
		}

		// 4. TRANSMITTER OVERRIDE (1100)
		if code == "1100" && len(apcontrollers) > 0 {
			la, _ := strconv.ParseFloat(p[1], 64)
			lo, _ := strconv.ParseFloat(p[2], 64)
			if math.Abs(la) > 0.1 {
				for i := batchStartIdx; i < len(apcontrollers); i++ {
					apcontrollers[i].Lat, apcontrollers[i].Lon = la, lo
				}
			}
		}

		// 5. TAXIWAY EXTRACTION
		if curAirport != nil {
			if code == "1201" { // Taxiway Node
				// Format: 1201 <lat> <lon> <node_id>
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					lat, _ := strconv.ParseFloat(fields[1], 64)
					lon, _ := strconv.ParseFloat(fields[2], 64)
					nodeID, _ := strconv.Atoi(fields[4])

					nodeBuffer[nodeID] = Coordinate{Lat: lat, Lon: lon}
				}
				continue
			}

			if code == "1202" {
				if canClearTaxiNames {
					curTaxiNames = []string{}
					canClearTaxiNames = false
				}
				fields := strings.Fields(line)
				if len(fields) >= 6 {
					name := fields[5]

					// 1. REJECT if it's a runway crossing (contains "/")
					// or if it's empty.
					if name == "" || strings.Contains(name, "/") {
						continue
					}

					// 2. PROCESS everything else (Alpha, Bravo, N11, LINK56, etc.)
					id1, _ := strconv.Atoi(fields[1])
					id2, _ := strconv.Atoi(fields[2])

					// Add to buffer for proximity math
					edgeBuffer = append(edgeBuffer, RawEdge{
						NodeA:    id1,
						NodeB:    id2,
						TaxiName: strings.TrimSpace(name),
					})

					// Add to the usage list so the 1204 block can "bless" it
					if !slices.Contains(curTaxiNames, name) {
						curTaxiNames = append(curTaxiNames, name)
					}
				}
				continue
			}

			if code == "1204" {
				fields := strings.Fields(line)

				if len(curTaxiNames) == 0 || len(fields) < 3 {
					continue
				}

				usage := fields[1]

				if usage == "departure" || usage == "arrival" || usage == "both" {

					rwyList := strings.Split(fields[2], ",")

					// Initialize UsageData map if it doesn't exist
					if curAirport.RunwayUsageData == nil {
						curAirport.RunwayUsageData = make(UsageMap)
					}

					for _, rwyID := range rwyList {
						// This creates the runway if it was previously unknown
						getOrCreateRunway(curAirport, rwyID)

						// Initialize the sub-map for this specific runway
						if curAirport.RunwayUsageData[rwyID] == nil {
							curAirport.RunwayUsageData[rwyID] = make(map[string]string)
						}

						for _, taxiName := range curTaxiNames {

							// Get what's currently stored for this taxiway/runway combo
							existing := curAirport.RunwayUsageData[rwyID][taxiName]

							// MERGE:
							if existing == "" || existing == usage {
								curAirport.RunwayUsageData[rwyID][taxiName] = usage
							} else {
								// If it was 'departure' and now 'arrival' (or vice versa), it's 'both'
								curAirport.RunwayUsageData[rwyID][taxiName] = "both"
							}

						}
					}
					canClearTaxiNames = true
				}
				continue
			}
		}
	}

	// file scanner complete - check for errors
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("scanner error: %w", err)
	}

	// Finalize the final block
	if curAirport != nil {
		finaliseAirport(curAirport, curLat, curLon, airportPoints, apcontrollers, curElev, nodeBuffer, edgeBuffer)
		allcontrollers = append(allcontrollers, apcontrollers...)
	}

	// FINAL MBB INITIALIZATION
	for i := range allcontrollers {
		c := allcontrollers[i]
		if c.Lat == 0 && c.Lon == 0 {
			logger.Log.Warnf("no controller location found for ICAO:%s Name: %s Role: %d\n", c.ICAO, c.Name, c.RoleID)
		}
		c.Airspaces = []Airspace{{
			Floor: -99999, Ceiling: 99999, Area: 0,
			MinLat: c.Lat, MaxLat: c.Lat, MinLon: c.Lon, MaxLon: c.Lon,
		}}
	}

	return allcontrollers, airports, nil
}

func finaliseAirport(ap *Airport, dLat, dLon float64, pts []aptPoint, apctrls []*Controller,
	elevation float64, nodeBuffer map[int]Coordinate, edgeBuffer []RawEdge) {

	var fLat, fLon float64

	// Prioritize Datum, then Centroid
	if dLat != 0 {
		fLat, fLon = dLat, dLon
	} else if len(pts) > 0 {
		var sLa, sLo float64
		for _, p := range pts {
			sLa += p.Lat
			sLo += p.Lon
		}
		fLat = sLa / float64(len(pts))
		fLon = sLo / float64(len(pts))
	}

	ap.Lat, ap.Lon = fLat, fLon
	ap.Elevation = elevation
	// Finalize the hub weights for the airport
	finaliseHubWeights(ap)
	// Finalise the runway usage data for the airport
	im := buildImportanceMap(edgeBuffer)
	nnm := buildNamedNodes(edgeBuffer, nodeBuffer, im)

	finaliseRuwayAccess(ap, nodeBuffer, edgeBuffer, nnm)
	// Finalize the parking spots for the airport (link to taxiway nodes, etc.)
	finaliseParking(ap, nnm)

	// Retroactive update for any controllers created with 0,0
	for _, ctrl := range apctrls {
		if ctrl.Lat == 0 {
			ctrl.Lat = ap.Lat
			ctrl.Lon = ap.Lon
		}
	}

	// validate
	for _, rwy := range ap.Runways {
		if rwy.Lat == 0 && rwy.Lon == 0 {
			util.LogWarnWithLabel("D9", "%s runway %s no location", ap.ICAO, rwy.Name)
		}
		if rwy.Length == 0 {
			util.LogWarnWithLabel("D9", "%s runway %s no length", ap.ICAO, rwy.Name)
		}
		if rwy.Width == 0 {
			util.LogWarnWithLabel("D9", "%s runway %s no width", ap.ICAO, rwy.Name)
		}
	}
}

func parseCIFP(cifpPath string, allFixes map[string]*Fix, ap *Airport) error {

	f, err := os.Open(cifpPath)
	if err != nil {
		return err
	}
	defer f.Close()

	scan := bufio.NewScanner(f)

	var currentRunway string
	var rw Runway // keep as value - not pointer
	var inApproach bool
	var currentAppType string

	saveApproach := func() {
		if !inApproach || currentRunway == "" {
			return
		}

		existing := ap.Runways[currentRunway]
		if existing == nil {
			existing = &Runway{}
			ap.Runways[currentRunway] = existing
		}

		// Only merge if this was a real approach (FAF or approach type)
		if rw.FAFalt > 0 || rw.HighestPrecisionApproach != "" {
			mergeRunway(existing, rw, currentAppType)
		} else {
			// Ensure runway entry exists, but keep it zeroed
			if _, ok := ap.Runways[currentRunway]; !ok {
				ap.Runways[currentRunway] = &Runway{}
			}
		}
	}

	var sawRunwayLeg bool

	var currentProc *pendingProc
	pendingProcs := []pendingProc{}

	lastSeq := 1

	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())

		// --- SID/STAR LOGIC ---
		if strings.HasPrefix(line, "SID:") || strings.HasPrefix(line, "STAR:") {
			fields := strings.Split(line, ",")
			if len(fields) < 12 {
				continue
			}

			// Extract Sequence (e.g., "010")
			seqPart := fields[0][strings.Index(fields[0], ":")+1:]
			seq, _ := strconv.Atoi(strings.TrimSpace(seqPart))

			procName := strings.TrimSpace(fields[2])
			procRwy := strings.TrimSpace(fields[3])
			var targetRwy string
			if procRwy == "ALL" {
				targetRwy = "ALL"
			} else {
				targetRwy = normaliseRunwayName(procRwy)
			}

			// 1. Initialize a new collector if this is the first leg
			if seq <= lastSeq {
				// If we were already working on one, save it before starting new
				if currentProc != nil {
					pendingProcs = append(pendingProcs, *currentProc)
					currentProc = nil
				}
				currentProc = &pendingProc{
					Name:       procName,
					RunwayName: targetRwy,
					Type:       0, // Default SID
				}
				if strings.HasPrefix(line, "STAR:") {
					currentProc.Type = 1
				}
			}
			lastSeq = seq

			if currentProc == nil {
				continue
			}

			// 2. Extract Leg Info
			fixID := strings.TrimSpace(fields[4])
			if fixID == "" {
				continue
			}
			regionID := strings.TrimSpace(fields[5])
			if regionID == "" {
				continue
			}

			if fData, ok := allFixes[fixID+"_"+regionID]; ok {
				pFix := ProcedureFix{
					Fix:            fData,
					ConstraintType: -1, // Initialize as none
				}

				// 3. Parse Alt Constraints (CIFP Columns 23-25)
				atOrAbove := normaliseCIFPAlt(strings.TrimSpace(fields[23]))
				atAlt := normaliseCIFPAlt(strings.TrimSpace(fields[24]))
				atOrBelow := normaliseCIFPAlt(strings.TrimSpace(fields[25]))

				if atAlt > 0 {
					pFix.ConstraintAlt = atAlt
					pFix.ConstraintType = 0
				} else if atOrAbove > 0 {
					pFix.ConstraintAlt = atOrAbove
					pFix.ConstraintType = 1
				} else if atOrBelow > 0 {
					pFix.ConstraintAlt = atOrBelow
					pFix.ConstraintType = 2
				}

				currentProc.Legs = append(currentProc.Legs, pFix)
			}
			continue
		}

		// If we hit a line that isn't a SID/STAR and we have a pending proc, wrap it up
		if currentProc != nil {
			pendingProcs = append(pendingProcs, *currentProc)
			currentProc = nil
		}

		if strings.HasPrefix(line, "RWY:") {
			parts := strings.Split(line, ";") // The physical data is usually after the semicolon
			if len(parts) < 2 {
				continue
			}

			// Physical Data: N51283900,W000290597,1014;
			dataFields := strings.Split(parts[1], ",")

			// Metadata Header: RWY:RW09L, , ,00079, ,IAA ,3,
			metaFields := strings.Split(parts[0], ",")

			rwyName := normaliseRunwayName(strings.TrimPrefix(metaFields[0], "RWY:"))

			// Create or get the existing runway
			rwEntry := ap.Runways[rwyName]
			if rwEntry == nil {
				rwEntry = &Runway{}
				ap.Runways[rwyName] = rwEntry
			}
			rwEntry.Name = rwyName

			if len(metaFields) >= 3 {
				length, _ := strconv.ParseFloat(strings.TrimSpace(metaFields[1]), 64)
				width, _ := strconv.ParseFloat(strings.TrimSpace(metaFields[2]), 64)
				rwEntry.Length = length
				rwEntry.Width = width
				if len(metaFields) >= 4 {
					// 1. Parse Heading (Token 3 in metaFields)
					if h, err := strconv.Atoi(strings.TrimSpace(metaFields[3])); err == nil {
						rwEntry.Heading = float64(h) // Already in degrees (e.g., 00079)
					}
				}
			}

			// 2. Parse Coordinates from dataFields
			if len(dataFields) >= 2 {
				rwEntry.Lat = parseCIFPCoord(dataFields[0])
				rwEntry.Lon = parseCIFPCoord(dataFields[1])
			}

			// 3. Parse Threshold Elevation (Token 2 in dataFields)
			if len(dataFields) >= 3 {
				// Remove trailing semicolon if present and parse
				elevStr := strings.TrimRight(dataFields[2], ";")
				if e, err := strconv.Atoi(elevStr); err == nil {
					// Note: CIFP elevation is often 1014 meaning 101.4, check your data source!
					rwEntry.ThresholdElevation = float64(e) / 10.0
				}
			}
			continue
		}

		if !strings.HasPrefix(line, "APPCH:") {
			continue
		}

		fields := strings.Split(line, ",")
		if len(fields) < 26 {
			continue
		}

		// Route/segment type: A, I, L, etc.
		routeType := strings.TrimSpace(fields[1])
		isFinal := routeType == "I" || routeType == "L" || routeType == "R" || routeType == "N"

		// Start of a new approach
		if strings.HasPrefix(fields[0], "APPCH:010") {
			// Save previous approach into runway
			saveApproach()

			// Reset for new approach
			inApproach = true
			currentRunway = ""
			rw = Runway{}
			currentAppType = ""
			sawRunwayLeg = false

			// Extract approach type from approach name
			name := strings.TrimSpace(fields[2])
			if name != "" {
				appType := string(name[0])
				if _, ok := approachRank[appType]; ok {
					currentAppType = appType
					rw.HighestPrecisionApproach = approachString[appType]
				}
			}
		}

		// Fix (can live in several fields)
		fix := ""
		fixCandidates := []int{4, 5, 13, 14}
		for _, idx := range fixCandidates {
			if idx < len(fields) {
				f := strings.TrimSpace(fields[idx])
				if f != "" {
					fix = f
					break
				}
			}
		}

		legType := strings.TrimSpace(fields[11])

		// Detect runway from RWxx fix
		if strings.HasPrefix(fix, "RW") {
			rwyName := normaliseRunwayName(fix)
			if rwyName != "" {
				currentRunway = rwyName
				// Ensure runway entry exists even before merging
				if _, ok := ap.Runways[currentRunway]; !ok {
					ap.Runways[currentRunway] = &Runway{}
				}
			}
		}

		// Altitude fields
		atOrAbove := strings.TrimSpace(fields[23])
		atAlt := strings.TrimSpace(fields[24])
		atOrBelow := strings.TrimSpace(fields[25])
		alt := lowestAltitudeOf(atAlt, atOrAbove, atOrBelow)

		// FAF detection (FFxx or FIxx)
		if (strings.HasPrefix(fix, "FF") || strings.HasPrefix(fix, "FI")) && alt >= 0 {
			if rw.FAFalt == 0 || alt < rw.FAFalt {
				rw.FAFalt = alt
			}
		}

		// Missed-approach detection (final segment only)
		isMA := isFinal && (strings.HasPrefix(fix, "MA") ||
			strings.HasPrefix(fix, "RW") ||
			strings.HasPrefix(fix, "FD") ||
			strings.HasPrefix(fix, "CI") ||
			legType == "CA" ||
			legType == "CF" ||
			legType == "HM" ||
			legType == "AF")

		// set missed approach altitude
		if isMA && alt > rw.MAalt {
			rw.MAalt = alt
		}

		// Detect RW leg
		if strings.HasPrefix(fix, "RW") {
			sawRunwayLeg = true
		}

		// Capture missed-approach heading
		if sawRunwayLeg && rw.MAHeading == 0 {
			if isMA {
				headingField := strings.TrimSpace(fields[20])
				if h, err := strconv.Atoi(headingField); err == nil {
					rw.MAHeading = h / 10
				}
			}
		}

		// MAFix: last MA leg's fix in final segment
		if isMA && !strings.HasPrefix(fix, "RW") {
			rw.MAFix = fix
		}
	}

	// Save last approach
	saveApproach()

	// --- STEP 2: POST-PROCESSING (Pairing & Geometry) ---
	for name, rw := range ap.Runways {
		recipName := getReciprocalName(name)
		recip, exists := ap.Runways[recipName]

		if exists {
			// 1. Calculate Physical Heading (True Heading)
			// This fixes the "77 vs 79" mirrored data issue
			rw.Heading = geometry.CalculateBearing(rw.Lat, rw.Lon, recip.Lat, recip.Lon)

			// 2. Calculate Length if missing
			if rw.Length == 0 {
				rw.Length = geometry.CalculateDistanceFeet(rw.Lat, rw.Lon, recip.Lat, recip.Lon)
			}

			// 3. Fallback for Elevation (if end B is 0.0, use end A)
			if rw.ThresholdElevation == 0 && recip.ThresholdElevation > 0 {
				rw.ThresholdElevation = recip.ThresholdElevation
			}

			// 4. Default Width if missing
			if rw.Width == 0 {
				// 150ft is the standard for most commercial runways.
				// We could even scale this based on the length:
				if rw.Length > constants.RunwayLengthLargeThreshM {
					rw.Width = constants.RunwayWidthLargeM
				} else {
					rw.Width = constants.RunwayWidthDefaultM
				}
			}
		}
	}

	finaliseProcedures(ap.Runways, pendingProcs)

	return scan.Err()
}

func finaliseProcedures(runways map[string]*Runway, pendingProcs []pendingProc) {

	for _, p := range pendingProcs {

		if len(p.Legs) == 0 {
			continue
		}

		newProc := &Procedure{
			Name: p.Name,
			Type: p.Type,
		}

		// Assign Entry/Exit based on sequence order
		newProc.Entry = &p.Legs[0]
		newProc.Exit = &p.Legs[len(p.Legs)-1]

		// Attach to the appropriate Runway(s)
		for name, rw := range runways {
			// If the procedure is for "ALL" runways or matches the name (e.g., "09L")
			if p.RunwayName == "ALL" || p.RunwayName == name {
				if p.Type == 0 { // SID
					rw.SIDs = append(rw.SIDs, newProc)
				} else { // STAR
					rw.STARs = append(rw.STARs, newProc)
				}
			}
		}
	}
}

func finaliseRuwayAccess(ap *Airport, nodeBuffer map[int]Coordinate, edgeBuffer []RawEdge, namedNodes []NamedNode) {

	for _, rwy := range ap.Runways {
		rwy.DepartureAccess = make(map[string]*AccessPoint)
		rwy.ArrivalAccess = make(map[string]*AccessPoint)

		for _, edge := range edgeBuffer {
			if edge.TaxiName == "" {
				continue
			}

			// Optimization: Get coordinates once
			coordA := nodeBuffer[edge.NodeA]
			coordB := nodeBuffer[edge.NodeB]

			// DEPARTURE HANDLING
			usage := getUsage(ap, edge.TaxiName, rwy.Name)
			if usage == "departure" || usage == "both" {

				// Helper for Departure logic to avoid code duplication
				processDeparture := func(nodeOnRwy, offRwyNode Coordinate, distToStart float64) {
					// NEW: XTD check ensures the node is actually ON the runway pavement
					xtd := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, nodeOnRwy.Lat, nodeOnRwy.Lon)

					if xtd < 0.03 { // 0.03 NM (~50m) is roughly half a runway width
						touching := findArterialFast(nodeOnRwy.Lat, nodeOnRwy.Lon, edge.TaxiName, namedNodes, 0.05, true)
						entryBrg := geometry.CalculateBearing(offRwyNode.Lat, offRwyNode.Lon, nodeOnRwy.Lat, nodeOnRwy.Lon)

						updateAccessPointIfCloser(rwy.DepartureAccess, edge.TaxiName, nodeOnRwy, distToStart, touching, entryBrg)
					}
				}

				// Check Node A
				distAStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordA.Lat, coordA.Lon)
				if distAStart < 0.2 {
					processDeparture(coordA, coordB, distAStart)
				}

				// Check Node B
				distBStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordB.Lat, coordB.Lon)
				if distBStart < 0.2 {
					processDeparture(coordB, coordA, distBStart)
				}
			}

			// ARRIVAL HANDLING
			if usage == "arrival" || usage == "both" {

				// Helper to evaluate a specific node for arrival
				processArrival := func(nodeOnRwy, nextNode Coordinate, distFromStart float64) {
					// 1. Centerline Escape Check
					xtdNext := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, nextNode.Lat, nextNode.Lon)
					xtdCurr := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, nodeOnRwy.Lat, nodeOnRwy.Lon)
					if xtdNext <= xtdCurr {
						return
					}

					distFromEnd := geometry.DistNM(rwy.EndLat, rwy.EndLon, nodeOnRwy.Lat, nodeOnRwy.Lon)
					isLastChance := distFromEnd < constants.LastExitBufferNM
					isSafeRollout := distFromStart > constants.MinArrivalDistNM

					if isSafeRollout || isLastChance {
						touching := findArterialFast(nodeOnRwy.Lat, nodeOnRwy.Lon, edge.TaxiName, namedNodes, 0.10, true)

						if touching != "" || isLastChance {
							rwyHeading := geometry.CalculateBearing(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon)
							exitBrg := geometry.CalculateBearing(nodeOnRwy.Lat, nodeOnRwy.Lon, nextNode.Lat, nextNode.Lon)

							angleDiff := math.Abs(rwyHeading - exitBrg)
							if angleDiff > 180 {
								angleDiff = 360 - angleDiff
							}

							// The Directional Filter
							maxAngle := 90.0 // Mid-runway: Forward exits only
							if isLastChance {
								maxAngle = 140.0
							} // End: Tight turns allowed

							if angleDiff <= maxAngle {
								acp := updateAccessPointIfCloser(rwy.ArrivalAccess, edge.TaxiName, nodeOnRwy, distFromEnd, touching, exitBrg)
								if acp != nil {
									// RETs (Rapid Exit Taxiways)
									acp.IsHighSpeed = (angleDiff <= constants.HighSpeedExitThresholdDeg)
									acp.IsNearEnd = isLastChance
								}
							}
						}
					}
				}

				// Check Node A (if it's on the runway)
				xtdA := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, coordA.Lat, coordA.Lon)
				if xtdA < 0.05 {
					distAStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordA.Lat, coordA.Lon)
					processArrival(coordA, coordB, distAStart)
				}

				// Check Node B (if it's on the runway)
				xtdB := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, coordB.Lat, coordB.Lon)
				if xtdB < 0.05 {
					distBStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordB.Lat, coordB.Lon)
					processArrival(coordB, coordA, distBStart)
				}
			}
		}
	}
}

func getUsage(ap *Airport, taxiName string, rwyName string) string {
	// Check if we have a specific rule for this runway-taxiway pair
	if rwyRules, exists := ap.RunwayUsageData[rwyName]; exists {
		if usage, found := rwyRules[taxiName]; found {
			return usage
		}
	}
	// Default fallback if no 1204 record exists for this taxiway
	return "both"
}

func finaliseParking(ap *Airport, namedNodes []NamedNode) {
	for _, park := range ap.Parking {
		// We pass the Coordinate, not a NodeID
		//park.TaxiwayName = findArterialNearParking(park.Lat, park.Lon, edgeBuffer, nodeBuffer, importance)
		park.TaxiwayName = findArterialFast(park.Lat, park.Lon, "", namedNodes, 0.05, false)
	}
}

func mergeRunway(existing *Runway, incoming Runway, appType string) {

	// BestApproach: keep the better-ranked one
	if appType != "" {
		if rankNew, ok := approachRank[appType]; ok {
			if existing.HighestPrecisionApproach == "" {
				existing.HighestPrecisionApproach = approachString[appType]
			} else {
				// find rank of existing
				var existingType string
				for t, s := range approachString {
					if s == existing.HighestPrecisionApproach {
						existingType = t
						break
					}
				}
				if existingType != "" {
					if rankOld, ok2 := approachRank[existingType]; ok2 && rankNew < rankOld {
						existing.HighestPrecisionApproach = approachString[appType]
					}
				}
			}
		}
	}

	// FAFalt: keep lowest non-zero
	if incoming.FAFalt > 0 && (existing.FAFalt == 0 || incoming.FAFalt < existing.FAFalt) {
		existing.FAFalt = incoming.FAFalt
	}

	// MAalt: keep highest
	if incoming.MAalt > existing.MAalt {
		existing.MAalt = incoming.MAalt
	}

	// MAHeading: keep first non-zero
	if existing.MAHeading == 0 && incoming.MAHeading != 0 {
		existing.MAHeading = incoming.MAHeading
	}

	// MAFix: keep first non-empty
	if existing.MAFix == "" && incoming.MAFix != "" {
		existing.MAFix = incoming.MAFix
	}

}

func lowestAltitudeOf(at, above, below string) int {
	vals := []string{at, above, below}
	best := -1
	for _, v := range vals {
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		if best == -1 || n < best {
			best = n
		}
	}
	return best
}

// normaliseRunwayName takes a raw runway identifier from CIFP which are prefixed with "RW" and normalises it to a standard format like "09L", "27R", etc.
func normaliseRunwayName(rw string) string {
	// fix is like "RW27", "RW27L", "RW9", "RW09R"
	if !strings.HasPrefix(rw, "RW") {
		return ""
	}

	core := rw[2:] // strip "RW"

	// Extract digits
	i := 0
	for i < len(core) && core[i] >= '0' && core[i] <= '9' {
		i++
	}

	num := core[:i]
	suffix := core[i:] // L/R/C if present

	// Pad single-digit runways
	if len(num) == 1 {
		num = "0" + num
	}

	return strings.TrimSpace(num + suffix)
}

// normaliseRunwayID ensures single digit runways match standard 3-char identifiers (e.g., "8" -> "08", "7L" -> "07L")
func normaliseRunwayID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 0 && id[0] >= '1' && id[0] <= '9' {
		// If the second char isn't a digit, it means it's a single digit (like "8" or "7L")
		if len(id) == 1 || (id[1] < '0' || id[1] > '9') {
			return "0" + id
		}
	}
	return id
}

func normaliseCIFPAlt(altStr string) int {
	altStr = strings.TrimSpace(altStr)
	if altStr == "" {
		return 0
	}

	// Handle Flight Levels (e.g., FL270)
	if strings.HasPrefix(altStr, "FL") {
		flVal, err := strconv.Atoi(altStr[2:])
		if err != nil {
			return 0
		}
		return flVal * 100 // FL270 -> 27,000 feet
	}

	// Handle standard feet (e.g., 06000)
	val, err := strconv.Atoi(altStr)
	if err != nil {
		return 0
	}

	// Handle ARINC 424 Metric Shift: Hundreds vs. Tens of Feet when < 1000
	if val < 1000 && val > 0 {
		return val * 10.0 // Scale up to true feet
	}
	return val
}

func getAirportICAObyPhaseClass(ac *Aircraft) string {
	switch ac.Flight.Phase.Class {
	case flightclass.PreflightParked, flightclass.Departing:
		return ac.Flight.Origin
	case flightclass.Cruising:
		return ""
	case flightclass.Arriving, flightclass.PostflightParked:
		return ac.Flight.Destination
	default:
		return ""
	}
}

func cleanAirportName(n string) string {
	n = strings.ToLower(n)
	n = n + " "

	// decompose accents (é becomes e + ´)
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	n, _, _ = transform.String(t, n)

	n = strings.ReplaceAll(n, " intl ", " ")
	n = strings.ReplaceAll(n, " y ", " e ")

	// If parentheses present, prefer the value inside them
	if i := strings.Index(n, "("); i != -1 {
		if j := strings.Index(n[i:], ")"); j != -1 {
			n = strings.TrimSpace(n[i+1 : i+j])
		}
	}
	if i := strings.Index(n, "/"); i != -1 {
		// take substring after first '/'
		n = strings.TrimSpace(n[i+1:])
	}

	// remove anything in square brackets, including the square brackets
	for {
		start := strings.Index(n, "[")
		if start == -1 {
			break
		}
		end := strings.Index(n[start:], "]")
		if end == -1 {
			// no closing bracket: trim everything from the opening bracket
			n = strings.TrimSpace(n[:start])
			break
		}
		// remove the bracketed section
		n = n[:start] + n[start+end+1:]
	}

	// if space + dash + space is found, remove the dash and everything after it
	if i := strings.Index(n, " - "); i != -1 {
		n = strings.TrimSpace(n[:i])
	}

	return strings.TrimSpace(n)
}

func parseCIFPCoord(coord string) float64 {
	coord = strings.TrimSpace(coord)
	// CIFP coords are typically N51283900 (9 chars) or W000290597 (10 chars)
	if len(coord) < 7 {
		return 0
	}

	dir := coord[0]

	// Latitude (N/S) uses 2 digits for degrees: [1:3]
	// Longitude (E/W) uses 3 digits for degrees: [1:4]
	degLen := 2
	if dir == 'E' || dir == 'W' {
		degLen = 3
	}

	// 1. Extract Degrees
	deg, _ := strconv.ParseFloat(coord[1:1+degLen], 64)

	// 2. Extract Minutes (always 2 digits following degrees)
	min, _ := strconv.ParseFloat(coord[1+degLen:3+degLen], 64)

	// 3. Extract Seconds (everything remaining)
	secStr := coord[3+degLen:]
	rawSec, _ := strconv.ParseFloat(secStr, 64)

	// Determine the power of 10 for the seconds denominator.
	// If input is "SSss" (len 4), we need to divide rawSec by 100 to get SS.ss.
	// If input is "SS" (len 2), we divide by 1 (10^0).
	// Then we divide by 3600 to get degrees.
	precisionPower := len(secStr) - 2
	if precisionPower < 0 {
		precisionPower = 0
	}

	actualSec := rawSec / math.Pow(10, float64(precisionPower))
	secDecimal := actualSec / 3600.0

	// 4. Combine into Decimal Degrees
	decimal := deg + (min / 60.0) + secDecimal

	// 5. Apply Hemisphere Sign
	if dir == 'S' || dir == 'W' {
		decimal = -decimal
	}

	return decimal
}

func getReciprocalName(name string) string {
	// Standard runway names are 2 or 3 chars: 09, 09L, 27R
	if len(name) < 2 {
		return ""
	}

	numStr := name[:2]
	suffix := name[2:] // L, R, or C

	num, _ := strconv.Atoi(numStr)
	recipNum := (num + 18)
	if recipNum > 36 {
		recipNum -= 36
	}

	recipSuffix := suffix
	if suffix == "L" {
		recipSuffix = "R"
	}
	if suffix == "R" {
		recipSuffix = "L"
	}

	return fmt.Sprintf("%02d%s", recipNum, recipSuffix)
}

// Make sure this is a method on your Airport struct
func finaliseHubWeights(ap *Airport) {
	ap.HubWeights = make(map[string]float64)

	tally := make(map[string]int)
	totalObservations := 0

	for _, spot := range ap.Parking {
		// Only count commercial/airline spots
		if spot.SizeType == "airline" {
			codes := strings.Fields(spot.AirlineCodes)
			for _, code := range codes {
				tally[code]++
				totalObservations++
			}
		}
	}

	// Convert tallies to percentage weights (0.0 to 1.0)
	if totalObservations > 0 {
		for code, count := range tally {
			ap.HubWeights[code] = float64(count) / float64(totalObservations)
		}
	}
}

func getOrCreateRunway(ap *Airport, rwyID string) *Runway {

	rwyID = normaliseRunwayID(rwyID)
	if rwy, exists := ap.Runways[rwyID]; exists {
		return rwy
	}

	// Create a new runway shell to hold taxiway data
	newRwy := &Runway{
		Name: rwyID,
	}
	ap.Runways[rwyID] = newRwy
	return newRwy
}

// updateAccessPointIfCloser updates the accessMap for the associated name with the provided data if
// there is no current entry or if distance is less than the currently associated AccessPoint.
// Returns the updated AccessPoint or nil if the map was not modified.
func updateAccessPointIfCloser(accessMap map[string]*AccessPoint, name string, coord Coordinate,
	dist float64, touching string, bearing float64) *AccessPoint {
	existing, exists := accessMap[name]

	// We update if it's the first time we see this name, or if this node is closer
	// to the reference point (Start for Departure, End for Arrival)
	if !exists || dist < existing.Dist {
		acp := &AccessPoint{
			Name:        name,
			Coord:       coord,
			TaxiwayName: touching,
			Bearing:     bearing,
			Dist:        dist, // Store temporarily to compare during finalization
		}
		accessMap[name] = acp
		return acp
	}

	return nil
}

// isIgnorable handles the generic "junk" names that appear in apt.dat
func isIgnorable(name string) bool {
	uName := strings.ToUpper(name)
	return strings.HasPrefix(uName, "LINK") ||
		strings.Contains(uName, "CONNECTOR") ||
		strings.Contains(uName, "UNNAMED")
}

func buildImportanceMap(edgeBuffer []RawEdge) map[string]int {
	importance := make(map[string]int)
	for _, edge := range edgeBuffer {
		if edge.TaxiName == "" {
			continue
		}
		// Each time a taxiway name appears, it gains "weight"
		importance[edge.TaxiName]++
	}
	return importance
}

func buildNamedNodes(edgeBuffer []RawEdge, nodeBuffer map[int]Coordinate, importance map[string]int) []NamedNode {
	nodes := make([]NamedNode, 0)
	seen := make(map[string]map[int]bool)

	for _, edge := range edgeBuffer {
		name := edge.TaxiName
		if name == "" || isIgnorable(name) {
			continue
		}

		// Only include "Backbone" taxiways.
		// If a taxiway only appears once or twice (like A13), it's a connector, not an arterial.
		if importance[name] < 3 {
			continue
		}

		if seen[name] == nil {
			seen[name] = make(map[int]bool)
		}
		for _, id := range []int{edge.NodeA, edge.NodeB} {
			if !seen[name][id] {
				c := nodeBuffer[id]
				nodes = append(nodes, NamedNode{c.Lat, c.Lon, name, importance[name]})
				seen[name][id] = true
			}
		}
	}
	return nodes
}

func findArterialFast(targetLat, targetLon float64, currentName string, namedNodes []NamedNode, initialRadiusNM float64, strictArterial bool) string {
	cleanCurrent := strings.TrimSpace(currentName)
	currentRadius := initialRadiusNM

	// We attempt up to 3 times, trebling the radius each time (e.g., 0.05 -> 0.15 -> 0.45)
	for attempt := 0; attempt < 3; attempt++ {
		var bestName string
		maxScore := -1.0

		degLimit := currentRadius / 45.0
		limitSq := degLimit * degLimit

		for i := range namedNodes {
			nn := &namedNodes[i]
			candidateName := nn.TaxiName

			// 1. Basic Exclusions
			if candidateName == "" || candidateName == cleanCurrent {
				continue
			}

			// 2. Hierarchy Logic (Fixes A1/A2/A3/A4 circular references)
			// If we are looking for a backbone, prioritize names by simplicity.
			if strictArterial {
				// Ignore long names like "LINK56" or "A13" if we want a backbone
				if len(candidateName) > 2 {
					continue
				}
				// If current is "A1", don't let it pick "A2".
				// A parent should be simpler (shorter) than the child.
				if len(cleanCurrent) >= 2 && len(candidateName) >= len(cleanCurrent) {
					continue
				}
			}

			// 3. Fast Pruning (Bounding Box)
			dLat := targetLat - nn.Lat
			dLon := targetLon - nn.Lon
			if (dLat*dLat)+(dLon*dLon) > limitSq {
				continue
			}

			// 4. Precise Distance Calculation
			dist := geometry.DistNM(targetLat, targetLon, nn.Lat, nn.Lon)
			if dist < currentRadius {
				// WEIGHTING: Single letters (A, B, S) get massive priority over double (A1, S2).
				// This ensures that even if A1 is closer to A2 than to Alpha,
				// the 'Gravity' of Alpha wins.
				weight := 1.0
				if len(candidateName) == 1 {
					weight = 1000.0
				} else if len(candidateName) == 2 {
					weight = 10.0
				}

				// SCORE: Weight / Distance Squared
				// The squared distance makes proximity the king for same-tier names,
				// but the weight allows high-tier names (Backbones) to win across gaps.
				score := weight / ((dist + 0.001) * (dist + 0.001))

				if score > maxScore {
					maxScore = score
					bestName = candidateName
				}
			}
		}

		// If a match is found in this radius tier, return it immediately.
		// This is why your performance is back under 8 seconds.
		if bestName != "" {
			return bestName
		}

		// Expand the search area for the next attempt
		currentRadius *= 3.0
	}

	return ""
}
