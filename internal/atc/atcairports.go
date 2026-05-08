package atc

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/pkg/geometry"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

type Airport struct {
	ICAO        string
	Name        string
	Lat         float64
	Lon         float64
	Elevation   float64 // feet
	TransAlt    int
	Region      string
	Runways     map[string]*Runway // keyed by "09L", "27R"
	RunwayUsageData    UsageMap	// temporary field used whilst loading data
	Holds       []*Hold
	Controllers []*Controller
	Parking     []*ParkingSpot
	HubWeights  map[string]float64 // Airline ICAO -> Strength (0.0 to 1.0)
	ClassCounts map[string]int     // "E": 20, "C": 100 (Total gates by size)
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
	DepartureTaxiways 		 map[string]struct{}
    ArrivalTaxiways   		 map[string]struct{}
	DepartureAccess  		 map[string]AccessPoint // Key: "A13", Value: AccessPoint{Coord, "Foxtrot"}
    ArrivalAccess    		 map[string]AccessPoint
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

type AccessPoint struct {
    Coord        Coordinate
    TaxiwayName  string // e.g., "Foxtrot" (The main taxiway this holding point feeds into)
	Dist 		 float64
}

type pendingProc struct {
	Name   string
	Type   int    // 0 = SID, 1 = STAR
	Runway string // e.g., "09L" or "ALL"
	Legs   []ProcedureFix
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

// returns nil if not found
func (s *Service) GetAirportRunway(icao, rwy string) *Runway {
	var r *Runway
	if icao != "" && rwy != "" {
		ap, found := s.Airports[icao]
		if found {
			r, _ = ap.Runways[rwy]
		}
	}
	return r
}

func (s *Service) GetAirport(icao string) *Airport {
	ap, exists := s.Airports[icao]
	if !exists {
		return nil
	}
	return ap
}

func loadAirports(dir string, airports map[string]*Airport, requiredAirports map[string]bool,
	airportHolds map[string][]*Hold, allHolds map[string]*Hold, allFixes map[string]*Fix) error {

	for icao := range requiredAirports {

		// Parse airport CIFP data for runway, approach and fixes data
		path := filepath.Join(dir, icao+".dat")
		rwyMap, err := parseCIFP(path, allFixes)
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
		
		ap.Holds = []*Hold{}

		// Add runways
		for rwyName, rwy := range rwyMap {
			aptRwy, exists := ap.Runways[rwyName]
			if !exists {
				aptRwy = getOrCreateRunway(ap, rwyName)
			}
			rwy.DepartureTaxiways = aptRwy.DepartureTaxiways
			rwy.ArrivalTaxiways = aptRwy.ArrivalTaxiways
			ap.Runways[rwyName] = &rwy
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
	var controllers []*Controller
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
			if curAirport != nil {
				if curAirport.ICAO == "EGLL" {
					logger.Log.Infof("breakpoint")
				}
				finaliseAirport(curAirport, curLat, curLon, airportPoints, controllers, curICAO, curElev, nodeBuffer, edgeBuffer)
				if curAirport.ICAO == "EGLL" {
					logger.Log.Infof("breakpoint")
				}
			}

			if len(p) >= 5 {
				curICAO = p[4]
				curName = cleanAirportName(strings.Join(p[5:], " "))
				curLat, curLon, transAlt, region = 0, 0, 0, ""
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
						Runways:	 make(map[string]*Runway),
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
		if curAirport != nil &&code == "1301" && curParking != nil {
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
			curAirport.Parking = append(curAirport.Parking, curParking)
			curParking = nil // Reset for next spot
			continue
		}

		// 2. GEOGRAPHY & METADATA (Universal Parsing)
		if curAirport != nil &&code == "1302" && len(p) == 3 {
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
			curAirport.TransAlt, curAirport.Region = transAlt, region
			continue
		}

		// Runway records (100 = Asphalt/Concrete, 101 = Water)
		if curAirport != nil && code == "100"  {
			// fields is often more reliable than manual slicing if columns shift
			fields := strings.Fields(line)
			
			if len(fields) >= 20 {
				// Parse Dimensions
				width, _ := strconv.ParseFloat(fields[1], 64)
				// Note: fields[3] is length in meters in the spec
				length, _ := strconv.ParseFloat(fields[3], 64)

				// Threshold 1 Coordinates
				lat1, _ := strconv.ParseFloat(fields[9], 64)
				lon1, _ := strconv.ParseFloat(fields[10], 64)
				
				// Threshold 2 Coordinates
				lat2, _ := strconv.ParseFloat(fields[18], 64)
				lon2, _ := strconv.ParseFloat(fields[19], 64)

				// --- Handle Primary End (e.g., 09L) ---
				id1 := fields[8]
				rwy1 := getOrCreateRunway(curAirport, id1)
				rwy1.Lat = lat1
				rwy1.Lon = lon1
				rwy1.EndLat = lat2 // The "Finish Line" for 09L
				rwy1.EndLon = lon2
				rwy1.Width = width
				rwy1.Length = length

				// --- Handle Reciprocal End (e.g., 27R) ---
				id2 := fields[17]
				if id2 != "" && id2 != "xxx" && id2 != "nil" {
					rwy2 := getOrCreateRunway(curAirport, id2)
					rwy2.Lat = lat2
					rwy2.Lon = lon2
					rwy2.EndLat = lat1 // The "Finish Line" for 27R is 09L's start
					rwy2.EndLon = lon1
					rwy2.Width = width
					rwy2.Length = length
				}

				// AIRPORT POSITION FALLBACK
				if lat1 != 0 && lat2 != 0 {
					airportPoints = append(airportPoints, aptPoint{lat1, lon1}, aptPoint{lat2, lon2})
				}
			}
			continue
		}

		// 3. FREQUENCY RECORDS
		if rID, ok := roleMap[code]; ok {
			isEnroute := rID >= 4
			if isRequiredAirport || isEnroute {
				fRaw, _ := strconv.Atoi(p[1])
				fNorm := normaliseFreq(fRaw)
				batchStartIdx = len(controllers)

				roles := []int{rID}
				if code == "1051" || code == "1054" {
					roles = []int{rID, 1, 2}
				}

				for _, r := range roles {
					c := &Controller{
						Name:    curName,
						ICAO:    curICAO,
						RoleID:  r,
						Freqs:   []int{fNorm},
						IsPoint: true,
						Lat:     curLat,
						Lon:     curLon,
					}
					controllers = append(controllers, c)
					if curAirport != nil {
						curAirport.Controllers = append(curAirport.Controllers, c)
					}
				}
			}
			continue
		}

		// 4. TRANSMITTER OVERRIDE (1100)
		if code == "1100" && len(controllers) > 0 {
			la, _ := strconv.ParseFloat(p[1], 64)
			lo, _ := strconv.ParseFloat(p[2], 64)
			if math.Abs(la) > 0.1 {
				for i := batchStartIdx; i < len(controllers); i++ {
					controllers[i].Lat, controllers[i].Lon = la, lo
				}
			}
		}

		// 5. TAXIWAY EXTRACTION
		if curAirport != nil {
			if code == "1201" {  // Taxiway Node
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

				if len(curTaxiNames) == 0 || len(fields) < 3 { continue }
		
				usage := fields[1]

				if usage == "departure" || usage == "arrival" || usage == "both" {
					
					rwyList := strings.Split(fields[2], ",")

					// Initialize UsageData map if it doesn't exist
					if curAirport.RunwayUsageData == nil {
						curAirport.RunwayUsageData = make(UsageMap)
					}

					for _, rwyID := range rwyList {
						// This creates the runway if it was previously unknown
						rwy := getOrCreateRunway(curAirport, rwyID)		
						
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

							switch usage {
							case "departure":
								rwy.DepartureTaxiways[taxiName] = struct{}{}
							case "arrival":
								rwy.ArrivalTaxiways[taxiName] = struct{}{}
							case "both":
								rwy.DepartureTaxiways[taxiName] = struct{}{}
								rwy.ArrivalTaxiways[taxiName] = struct{}{}
							}
						}
					}
					canClearTaxiNames = true
				}
				continue
			}
		}
	}

	// Finalize final block
	if curAirport != nil {
		finaliseAirport(curAirport, curLat, curLon, airportPoints, controllers, curICAO, curElev, nodeBuffer, edgeBuffer)
	}

	// FINAL MBB INITIALIZATION
	for i := range controllers {
		c := controllers[i]
		if c.Lat == 0 && c.Lon == 0 {
			logger.Log.Warnf("no location found for ICAO:%s Name: %s Role: %d\n", c.ICAO, c.Name, c.RoleID)
		}
		c.Airspaces = []Airspace{{
			Floor: -99999, Ceiling: 99999, Area: 0,
			MinLat: c.Lat, MaxLat: c.Lat, MinLon: c.Lon, MaxLon: c.Lon,
		}}
	}

	return controllers, airports, nil
}

func finaliseAirport(ap *Airport, dLat, dLon float64, pts []aptPoint, allCtrls []*Controller, 
						icao string, elevation float64, nodeBuffer map[int]Coordinate, edgeBuffer []RawEdge) {
	
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
	//TODO if we still have no lat/lon for the airport we could use average of parking spots

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
	for i := len(allCtrls) - 1; i >= 0; i-- {
		if allCtrls[i].ICAO != icao {
			if i < len(allCtrls)-100 {
				break
			} // Optimization
			continue
		}
		if allCtrls[i].Lat == 0 {
			allCtrls[i].Lat, allCtrls[i].Lon = fLat, fLon
		}
	}
}

func parseCIFP(cifpPath string, allFixes map[string]*Fix) (map[string]Runway, error) {

	f, err := os.Open(cifpPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	runways := make(map[string]Runway)
	scan := bufio.NewScanner(f)

	var currentRunway string
	var rw Runway
	var inApproach bool
	var currentAppType string

	saveApproach := func() {
		if !inApproach || currentRunway == "" {
			return
		}

		existing := runways[currentRunway]

		// Only merge if this was a real approach (FAF or approach type)
		if rw.FAFalt > 0 || rw.HighestPrecisionApproach != "" {
			runways[currentRunway] = mergeRunway(existing, rw, currentAppType)
		} else {
			// Ensure runway entry exists, but keep it zeroed
			if _, ok := runways[currentRunway]; !ok {
				runways[currentRunway] = Runway{}
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
					Name:   procName,
					Runway: targetRwy,
					Type:   0, // Default SID
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
			rwEntry := runways[rwyName]
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

			runways[rwyName] = rwEntry
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
			rwy := normaliseRunwayName(fix)
			if rwy != "" {
				currentRunway = rwy
				// Ensure runway entry exists even before merging
				if _, ok := runways[currentRunway]; !ok {
					runways[currentRunway] = Runway{}
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
	for name, rw := range runways {
		recipName := getReciprocalName(name)
		recip, exists := runways[recipName]

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
				if rw.Length > 6000 {
					rw.Width = 150.0
				} else {
					rw.Width = 100.0
				}
			}

			runways[name] = rw
		}
	}

	finaliseProcedures(runways, pendingProcs)

	return runways, scan.Err()
}

func finaliseProcedures(runways map[string]Runway, pendingProcs []pendingProc) {

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
			if p.Runway == "ALL" || p.Runway == name {
				if p.Type == 0 { // SID
					rw.SIDs = append(rw.SIDs, newProc)
				} else { // STAR
					rw.STARs = append(rw.STARs, newProc)
				}
				// IMPORTANT: Write the modified Runway struct back to the map
				runways[name] = rw
			}
		}
	}
}

func finaliseRuwayAccess(ap *Airport, nodeBuffer map[int]Coordinate, edgeBuffer []RawEdge, namedNodes []NamedNode) {
    for _, rwy := range ap.Runways {
        rwy.DepartureAccess = make(map[string]AccessPoint)
        rwy.ArrivalAccess = make(map[string]AccessPoint)

        totalRwyLen := geometry.DistNM(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon)
        arrivalZoneStart := totalRwyLen * 0.60
        const proximityThreshold = 0.15 

        for _, edge := range edgeBuffer {
            if edge.TaxiName == "" { continue }

            // Optimization: Get coordinates once
            coordA := nodeBuffer[edge.NodeA]
            coordB := nodeBuffer[edge.NodeB]

            // DEPARTURE HANDLING
            usage := getUsage(ap, edge.TaxiName, rwy.Name)
            if usage == "departure" || usage == "both" {
                // Check Node A
                distAStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordA.Lat, coordA.Lon)
                if distAStart < proximityThreshold { 
                    // CRITICAL: Pass edge.TaxiName to exclude it from the search!
                    touching := findArterialFast(coordA.Lat, coordA.Lon, edge.TaxiName, namedNodes, 0.3, true)
                    updateAccessPointIfCloser(rwy.DepartureAccess, edge.TaxiName, coordA, distAStart, touching) 
                }
                // Check Node B
                distBStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordB.Lat, coordB.Lon)
                if distBStart < proximityThreshold { 
                    touching := findArterialFast(coordB.Lat, coordB.Lon, edge.TaxiName, namedNodes, 0.3, true)
                    updateAccessPointIfCloser(rwy.DepartureAccess, edge.TaxiName, coordB, distBStart, touching) 
                }
            }

            // ARRIVAL HANDLING
            if usage == "arrival" || usage == "both" {
                // Only do CrossTrack if we are actually in the arrival zone (Cheap check first)
                distAStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordA.Lat, coordA.Lon)
                if distAStart > arrivalZoneStart {
                    xtdA := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, coordA.Lat, coordA.Lon)
                    if xtdA < 0.05 { 
                        touching := findArterialFast(coordA.Lat, coordA.Lon, edge.TaxiName, namedNodes, 0.3, true)
                        distAEnd := geometry.DistNM(rwy.EndLat, rwy.EndLon, coordA.Lat, coordA.Lon)
                        updateAccessPointIfCloser(rwy.ArrivalAccess, edge.TaxiName, coordA, distAEnd, touching)
                    }
                }
                distBStart := geometry.DistNM(rwy.Lat, rwy.Lon, coordB.Lat, coordB.Lon)
                if distBStart > arrivalZoneStart {
                    xtdB := geometry.CrossTrackDistance(rwy.Lat, rwy.Lon, rwy.EndLat, rwy.EndLon, coordB.Lat, coordB.Lon)
                    if xtdB < 0.05 { 
                        touching := findArterialFast(coordB.Lat, coordB.Lon, edge.TaxiName, namedNodes, 0.3, true)
                        distBEnd := geometry.DistNM(rwy.EndLat, rwy.EndLon, coordB.Lat, coordB.Lon)
                        updateAccessPointIfCloser(rwy.ArrivalAccess, edge.TaxiName, coordB, distBEnd, touching)
                    }
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
		park.TaxiwayName = findArterialFast(park.Lat, park.Lon, "", namedNodes, 0.15, false)
    }
}

func mergeRunway(existing, incoming Runway, appType string) Runway {

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

	return existing
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

	return num + suffix
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
	if len(coord) < 9 {
		return 0
	}

	dir := coord[0]
	// For Lat (N/S), deg is 2 digits. For Lon (E/W), deg is 3 digits.
	degLen := 2
	if dir == 'E' || dir == 'W' {
		degLen = 3
	}

	deg, _ := strconv.ParseFloat(coord[1:1+degLen], 64)
	min, _ := strconv.ParseFloat(coord[1+degLen:3+degLen], 64)
	sec, _ := strconv.ParseFloat(coord[3+degLen:], 64)

	// DD + MM/60 + SS.ss/3600
	decimal := deg + (min / 60.0) + (sec / 360000.0)

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
    if rwy, exists := ap.Runways[rwyID]; exists {
        return rwy
    }

    // Create a new runway shell to hold taxiway data
    newRwy := &Runway{
        Name:        		rwyID,
        DepartureTaxiways: 	make(map[string]struct{}),
        ArrivalTaxiways:   	make(map[string]struct{}),
    }
    ap.Runways[rwyID] = newRwy
    return newRwy
}

func updateAccessPointIfCloser(accessMap map[string]AccessPoint, name string, coord Coordinate, dist float64, touching string) {
    existing, exists := accessMap[name]
    
    // We update if it's the first time we see this name, or if this node is closer 
    // to the reference point (Start for Departure, End for Arrival)
    if !exists || dist < existing.Dist { 
        accessMap[name] = AccessPoint{
            Coord:       coord,
            TaxiwayName: touching,
            Dist:        dist, // Store temporarily to compare during finalization
        }
    }
}

func findArterialNearby(nodeID int, currentName string, edgeBuffer []RawEdge, nodeBuffer map[int]Coordinate, importance map[string]int) string {
    targetCoord, ok := nodeBuffer[nodeID]
    if !ok { return "" }
    
    var bestName string
    maxWeight := -1
    
    // 0.08 NM (~150m) allows us to bridge the gap across wide grass islands
    // or through multiple "LINK" segments.
    const searchRadius = 0.08 

    for _, edge := range edgeBuffer {
        name := edge.TaxiName
        
        // 1. Skip empty names, the entrance itself, and "junk" segments
        if name == "" || name == currentName || isIgnorable(name) {
            continue
        }

        // 2. Measure perpendicular distance to this candidate taxiway
        dist := geometry.CrossTrackDistance(
            nodeBuffer[edge.NodeA].Lat, nodeBuffer[edge.NodeA].Lon,
            nodeBuffer[edge.NodeB].Lat, nodeBuffer[edge.NodeB].Lon,
            targetCoord.Lat, targetCoord.Lon,
        )

        if dist < searchRadius {
            weight := importance[name]
            // 3. Higher weight = Main Arterial (Alpha/Bravo/Foxtrot)
            if weight > maxWeight {
                maxWeight = weight
                bestName = name
            }
        }
    }
    return bestName
}

// isIgnorable handles the generic "junk" names that appear in apt.dat
func isIgnorable(name string) bool {
    uName := strings.ToUpper(name)
    return strings.HasPrefix(uName, "LINK") || 
           strings.Contains(uName, "CONNECTOR") || 
           strings.Contains(uName, "UNNAMED")
}

func findArterialNearParking(lat, lon float64, edgeBuffer []RawEdge, nodeBuffer map[int]Coordinate, importance map[string]int) string {
    var bestName string
    maxScore := -1.0
    
    const searchRadius = 0.06

    for _, edge := range edgeBuffer {
        name := edge.TaxiName
        if name == "" || isIgnorable(name) { continue }

        dist := geometry.CrossTrackDistance(
            nodeBuffer[edge.NodeA].Lat, nodeBuffer[edge.NodeA].Lon,
            nodeBuffer[edge.NodeB].Lat, nodeBuffer[edge.NodeB].Lon,
            lat, lon,
        )

        if dist < searchRadius {
            weight := float64(importance[name])
            
            // Exponential Distance Penalty: 
            // We divide the weight by the distance squared (plus a small buffer to avoid div by zero).
            // This means a taxiway twice as far away needs 4x the importance to win.
            distScore := weight / ((dist * dist) + 0.001)

            if distScore > maxScore {
                maxScore = distScore
                bestName = name
            }
        }
    }
    return bestName
}

func buildImportanceMap(edgeBuffer []RawEdge) map[string]int {
    importance := make(map[string]int)
    for _, edge := range edgeBuffer {
        if edge.TaxiName == "" { continue }
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
        if name == "" || isIgnorable(name) { continue }

        // Only include "Backbone" taxiways.
        // If a taxiway only appears once or twice (like A13), it's a connector, not an arterial.
        if importance[name] < 3 { 
            continue 
        }

        if seen[name] == nil { seen[name] = make(map[int]bool) }
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

func findArterialFast(targetLat, targetLon float64, currentName string, namedNodes []NamedNode, searchRadiusNM float64, strictArterial bool) string {
    var bestName string
    maxScore := -1.0
    
    degLimit := searchRadiusNM / 45.0 
    limitSq := degLimit * degLimit

    for i := range namedNodes {
        nn := &namedNodes[i]
        
        // 1. Basic Exclusions
        if nn.TaxiName == "" || nn.TaxiName == currentName {
            continue
        }

        // 2. THE RUNWAY FIX: If strict, only accept A, B, K, M, etc.
        // This prevents "A" from picking "A3" and "LINK56" from picking "A13"
        if strictArterial && len(nn.TaxiName) > 2 {
            continue
        }

        // 3. Distance Check
        dLat := targetLat - nn.Lat
        dLon := targetLon - nn.Lon
        approxDistSq := (dLat * dLat) + (dLon * dLon)
        if approxDistSq > limitSq {
            continue
        }

        dist := geometry.DistNM(targetLat, targetLon, nn.Lat, nn.Lon)
        if dist < searchRadiusNM {
            weight := float64(nn.Importance)
            
            // Give 1-2 char names a boost regardless
            if len(nn.TaxiName) <= 2 {
                weight *= 100.0 
            }

            score := weight / (dist + 0.005)
            if score > maxScore {
                maxScore = score
                bestName = nn.TaxiName
            }
        }
    }
    return bestName
}