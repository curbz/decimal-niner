package atc

import (
	"time"
)

type UserState struct {
	NearestICAO      string
	Position         Position
	ActiveFacilities map[int]*Controller // Key: 1 for COM1, 2 for COM2
	TunedFreqs       map[int]int         // Key: 1 for COM1, 2 for COM2
	TunedFacilities  map[int]int         // Key: 1 for COM1, 2 for COM2
}

// +---------------+
// | Weather types |
// +---------------+
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
	Flight        float64
	Sealevel      float64
	TransitionAlt int // TODO: remove from here, this is fixed value per ICAO
}

type AirlineInfo struct {
	AirlineName string `json:"airline_name"`
	Callsign    string `json:"callsign"`
	CountryCode string `json:"icao_country_code"`
}

// +----------------------------------------------------------------------------------------+
// | Aircraft and nested types. Do not use unexported fields as deep copy will exclude them |
// +----------------------------------------------------------------------------------------+
type Aircraft struct {
	Flight       Flight
	Type         string
	SizeClass    string
	Code         string
	Airline      string
	Registration string
}

type Flight struct {
	Position        Position
	Number          int
	TaxiRoute       string
	Origin          string
	Destination     string
	Phase           Phase
	Comms           Comms
	AltClearance    int
	AssignedParking string
	AssignedRunway  string
	Squawk          string
}

type Position struct {
	Lat      float64
	Long     float64
	Altitude float64
	Heading  float64
}

type Phase struct {
	Class      PhaseClass
	Current    int // TODO: Current and Previous should really be FlightPhase type
	Previous   int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}

type Comms struct {
	Callsign    string
	Controller  *Controller
	CountryCode string
}

type PhaseClass int

const (
	Unknown          PhaseClass = iota - 1 // -1
	PreflightParked                        // 0
	Departing                              // 1 = all flight phases from startup to climb out
	Cruising                               // 2
	Arriving                               // 3 = all flight phases from approach to shutdown
	PostflightParked                       // 4
)

func (fc PhaseClass) String() string {
	return [...]string{
		"Unknown",
		"PreflightParked",
		"Departing",
		"Cruising",
		"Arriving",
		"PostflightParked",
	}[fc+1]
}

// +----------------------------------------------------------+
// | ATCMessage represents a single ATC communication message |
// +----------------------------------------------------------+
type ATCMessage struct {
	ControllerICAO string
	AircraftSnap   *Aircraft
	Role           string
	Text           string
	CountryCode    string
	ControllerName string
}

// +------------------------------+
// | ATC Controller related types |
// +------------------------------+
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

type AirportCoords struct {
	Lat  float64
	Lon  float64
	Name string
}
