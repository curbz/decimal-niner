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
	TransitionAlt int		// TODO: set this value, but lookup required on ICAO
}

type AirlineInfo struct {
	AirlineName string `json:"airline_name"`
	Callsign    string `json:"callsign"`
	CountryCode string `json:"icao_country_code"`
}

type Aircraft struct {
	Flight       Flight
	Type         string
	Class        string
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
	Current    int
	Previous   int // used for detecting changes, previous refers to last update and not necessarily the actual previous phase
	Transition time.Time
}

type Comms struct {
	Callsign         string
	Controller       *Controller
	CountryCode      string
	LastTransmission string
	LastInstruction  string
}

// ATCMessage represents a single ATC communication message
type ATCMessage struct {
	ICAO        string
	Callsign    string
	Role        string
	Text        string
	FlightPhase int
	CountryCode string
}

type Airspace struct {
	Floor, Ceiling float64
	Points         [][2]float64
}

type Controller struct {
	Name, ICAO string
	RoleID     int
	Freqs      []int
	Lat, Lon   float64
	IsPoint    bool
	IsRegion   bool
	Airspaces  []Airspace
}

type PhaseFacility struct {
	atcPhase string
	roleId   int
}
