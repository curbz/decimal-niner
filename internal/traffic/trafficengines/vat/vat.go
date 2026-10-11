package vat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightclass"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/flightplan"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
	"github.com/curbz/decimal-niner/pkg/util"
	"github.com/mohae/deepcopy"
)

// TODO review: flight phases for VAT will not fully align with the phases in the flightphase package,
// so we may need to create a new set of flight phases for VAT and then map them to the flightphase package phases for compatibility with the rest of the system.
const (
	LastCheckedPositionVerticalThreshold = 1000.0 // altitude in feet that the aircraft must have descended since the last check to trigger cruise top of descent logic

	FP_Unknown  int = iota - 1
	FP_Cruise       // 0 - Normal cruise phase.
	FP_Approach     // 1 - Positioning from cruise to the runway.
	FP_Final        // 2 - Gear down on final approach.
	FP_TaxiIn       // 3 - Any ground movement after touchdown.
	FP_Shutdown     // 4 - Short period of spooling down engines/electrics.
	FP_Parked       // 5 - Long period parked.
	FP_Startup      // 6 - Short period of spooling up engines/electrics.
	FP_TaxiOut      // 7 - Any ground movement from the gate to the runway.
	FP_Depart       // 8 - Initial ground roll and first part of climb.
	FP_GoAround     // 9 - Unplanned transition from approach to cruise.
	FP_Climbout     // 10 - Remainder of climb, gear up.
	FP_Braking      // 11 - Short period from touchdown to when fast-taxi speed is reached.
	FP_Holding      // 12 - Holding (waiting for a flow to complete changing)
)

type VATconfig struct {
	VAT struct {
		RefreshIntervalSeconds float64 `yaml:"refresh_interval_seconds"`
	} `yaml:"vat"`
}

type VATTraffic struct {
	traffic.CommonTrafficEngine
	RefreshIntervalSeconds float64
	AircraftMap            map[int]*atc.Aircraft
	initialized            bool
}

type vatAircraftData struct {
	SchemaVersion int        `json:"schemaVersion"`
	PlaneCount    int        `json:"planeCount"`
	Planes        []vatPlane `json:"planes"`
}

type vatPlane struct {
	PlaneID             int     `json:"planeID"`
	FlightNumber        string  `json:"flightNumber"`
	OperatorName        string  `json:"operatorName"`
	AircraftICAO        string  `json:"aircraftICAO"`
	SpawnedAircraftICAO string  `json:"spawnedAircraftICAO"`
	OriginICAO          string  `json:"originICAO"`
	DestinationICAO     string  `json:"destinationICAO"`
	State               string  `json:"state"`
	Latitude            float64 `json:"latitude"`
	Longitude           float64 `json:"longitude"`
	AltitudeFt          float64 `json:"altitudeFt"`
	SpeedKt             float64 `json:"speedKt"`
	HeadingDeg          float64 `json:"headingDeg"`
	CruiseAltitudeFt    float64 `json:"cruiseAltitudeFt"`
}

func New(cfgPath string) (atc.TrafficEngine, error) {

	cfg, err := util.LoadConfig[VATconfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading Virtual Air Traffic engine configuration file: %v", err)
		return nil, err
	}

	simdata.DRVATAircraftJSON = "virtualairtraffic/traffic/planes_json"

	subscribeDatarefs := []xpapimodel.Dataref{
		{Name: simdata.DRVATAircraftJSON,
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "json"},
	}
	simdata.SubscribeDatarefs = append(simdata.SubscribeDatarefs, subscribeDatarefs...)

	te := &VATTraffic{
		RefreshIntervalSeconds: cfg.VAT.RefreshIntervalSeconds,
		AircraftMap:            make(map[int]*atc.Aircraft),
	}
	return te, nil
}

func (e *VATTraffic) Start() {
	// no-op for VAT
}

func (e *VATTraffic) SetATCService(atcService *atc.Service) {
	e.AtcService = atcService
	if atcService != nil {
		atcService.RegisterTrafficEngine(e)
	}
}

// Enrich will apply additional data to the aircraft and should be called by the ATC Service on aircraft phase change
// TODO review - may not be implementable, potnetially a no-op for VAT
func (e *VATTraffic) Enrich(ac *atc.Aircraft, ap *atc.Airport) {
	if ac == nil || ap == nil || ac.Flight.AssignedRunway == nil {
		return
	}

	switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
	case flightphase.Final, flightphase.Braking, flightphase.TaxiIn:
		if ac.Flight.AssignedRunway.ArrivalAccess == nil {
			e.AtcService.AssignRunwayAccessPoint(ac, ap, atc.ARRIVAL_CONTEXT)
		}
	}

	if ac.Flight.Phase.Current <= flightphase.TaxiOut.Index() {
		if ac.Flight.AssignedRunway.DepartureAccess == nil {
			e.AtcService.AssignRunwayAccessPoint(ac, ap, atc.DEPARTURE_CONTEXT)
		}
	}

	if ac.Flight.Phase.Current <= flightphase.Departure.Index() {
		if ac.Flight.AssignedSID == nil {
			e.AtcService.AssignSID(ac, ap, ac.Flight.AssignedRunway)
		}
	}

	if ac.Flight.Phase.Current >= flightphase.Cruise.Index() && ac.Flight.Phase.Current <= flightphase.Approach.Index() {
		if ac.Flight.AssignedSTAR == nil {
			e.AtcService.AssignSTAR(ac, ap, ac.Flight.AssignedRunway)
		}
	}
}

// TODO review - may not be implementable, potnetially a no-op for VAT
func (e *VATTraffic) CheckForSubPhaseChange(ac *atc.Aircraft) {

	// if last check position has not yet been set, set it now so that we have something to compare against and return
	if ac.Flight.LastCheckedPosition.Lat == 0 && ac.Flight.LastCheckedPosition.Long == 0 {
		ac.Flight.LastCheckedPosition = ac.Flight.Position
		return
	}

	switch flightphase.FlightPhase(ac.Flight.Phase.Current) {
	case flightphase.Cruise:
		// check for possible sector change
		e.CheckForCruiseSectorChange(ac)
		// check for TOD
		e.CheckForTOD(ac)
	}
}

// TODO review - may not be implementable, potnetially a no-op for VAT
func (e *VATTraffic) CheckForTOD(ac *atc.Aircraft) {

	if ac.Flight.ClearedTOD {
		return
	}

	descent := ac.Flight.LastCheckedPosition.Altitude - ac.Flight.Position.Altitude
	// Only notify if descent is more than threshold
	if descent >= LastCheckedPositionVerticalThreshold {
		// set new state to trigger subphase
		ac.Flight.ClearedTOD = true
		// deep copy and transmit
		util.LogWithLabel(ac.Registration, "TOD detected, aircraft descending")
		// creat snapshot of aircraft state for phrase generation
		v := deepcopy.Copy(ac)
		acSnap, ok := v.(*atc.Aircraft)
		if !ok {
			util.LogWarnWithLabel(ac.Registration, "failed to deepcopy aircraft snapshot for TOD subphase; skipping phrase generation")
		} else {
			util.GoSafe(func() {
				// +-----------------------------------------------------------------+
				// | Only use acSnap to reference the aircraft within the go routine |
				// +-----------------------------------------------------------------+
				e.AtcService.Transmit(e.AtcService.UserState, acSnap)
			})
		}
		// update position
		ac.Flight.LastCheckedPosition = ac.Flight.Position
	}

}

func (e *VATTraffic) GetFlightPlanPath() string {
	return ""
}

func (e *VATTraffic) LoadFlightPlans(dirPath string) (map[string][]flightplan.ScheduledFlight, map[string]bool) {
	//for VAT this will be a no-op since flight origin, destination and cruise are all provided by the the json dataref, so return an empty map and an empty map of bools
	return make(map[string][]flightplan.ScheduledFlight), make(map[string]bool)
}

func (e *VATTraffic) RequiresAircraftData() bool {
	return true
}

func (e *VATTraffic) DatarefsToWrite() []xpapimodel.Dataref {
	return []xpapimodel.Dataref{{
		Name:  "virtualairtraffic/traffic/planes_freq_s",
		Value: e.RefreshIntervalSeconds,
	}}
}

func (e *VATTraffic) HandleAircraftData(datarefs map[int]*xpapimodel.Dataref) {
	if e.AtcService == nil {
		logger.Log.Error("cannot update VAT aircraft before the ATC service is set")
		return
	}

	dataref := getDatarefByName(datarefs, simdata.DRVATAircraftJSON)
	if dataref == nil {
		logger.Log.Errorf("VAT aircraft JSON dataref %q was not found", simdata.DRVATAircraftJSON)
		return
	}

	snapshot, err := decodeVATAircraftData(dataref.Value)
	if err != nil {
		logger.Log.Errorf("could not decode VAT aircraft data: %v", err)
		return
	}
	if snapshot.SchemaVersion != 1 {
		logger.Log.Errorf("unsupported VAT aircraft data schema version %d", snapshot.SchemaVersion)
		return
	}
	if snapshot.PlaneCount != len(snapshot.Planes) {
		logger.Log.Errorf("VAT aircraft data plane count mismatch: declared %d, received %d", snapshot.PlaneCount, len(snapshot.Planes))
		return
	}
	if e.AircraftMap == nil {
		e.AircraftMap = make(map[int]*atc.Aircraft)
	}

	seen := make(map[int]struct{}, len(snapshot.Planes))
	for _, plane := range snapshot.Planes {
		if plane.PlaneID <= 0 {
			logger.Log.Error("VAT aircraft record has invalid planeID")
			return
		}
		if _, exists := seen[plane.PlaneID]; exists {
			logger.Log.Errorf("VAT aircraft data contains duplicate planeID %d", plane.PlaneID)
			return
		}
		seen[plane.PlaneID] = struct{}{}
	}

	now := e.AtcService.GetCurrentZuluTime()
	updated := make([]*atc.Aircraft, 0, len(snapshot.Planes))
	for _, plane := range snapshot.Planes {
		aircraft, exists := e.AircraftMap[plane.PlaneID]
		if !exists {
			aircraft = newVATAircraft(plane, now, e.AtcService)
			e.AircraftMap[plane.PlaneID] = aircraft
		}

		aircraft.Type = plane.SpawnedAircraftICAO
		aircraft.Code = plane.AircraftICAO
		if aircraft.Code == "" {
			aircraft.Code = plane.SpawnedAircraftICAO
		}
		aircraft.Flight.Number = flightNumber(plane.FlightNumber)
		aircraft.Flight.Origin = plane.OriginICAO
		aircraft.Flight.Destination = plane.DestinationICAO
		aircraft.Flight.Position = atc.Position{
			Lat:      plane.Latitude,
			Long:     plane.Longitude,
			Altitude: plane.AltitudeFt,
			Heading:  plane.HeadingDeg,
		}
		aircraft.Flight.GroundSpeed = plane.SpeedKt
		aircraft.Flight.CruiseAlt = int(plane.CruiseAltitudeFt)
		aircraft.Flight.Comms.Callsign = plane.FlightNumber
		setVATAirline(aircraft, plane.OperatorName, e.AtcService)

		if phase, ok := vatFlightPhase(plane.State, aircraft.Flight.Phase.Current); ok {
			aircraft.Flight.Phase.Current = phase.Index()
		} else {
			logger.Log.Warnf("unrecognized VAT aircraft state %q for planeID %d", plane.State, plane.PlaneID)
		}
		updated = append(updated, aircraft)
	}

	for _, aircraft := range updated {
		if !e.initialized {
			aircraft.Flight.Phase.Previous = aircraft.Flight.Phase.Current
			aircraft.Flight.Phase.Transition = now
			continue
		}
		if aircraft.Flight.Phase.Current != aircraft.Flight.Phase.Previous {
			e.AtcService.NotifyFlightPhaseChange(aircraft)
			aircraft.Flight.Phase.Previous = aircraft.Flight.Phase.Current
			aircraft.Flight.Phase.Transition = now
		} else {
			e.CheckForSubPhaseChange(aircraft)
		}
	}
	for planeID := range e.AircraftMap {
		if _, exists := seen[planeID]; !exists {
			delete(e.AircraftMap, planeID)
		}
	}
	e.initialized = true
}

func decodeVATAircraftData(value any) (vatAircraftData, error) {
	var raw []byte
	switch v := value.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	case json.RawMessage:
		raw = v
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return vatAircraftData{}, fmt.Errorf("marshal dataref value of type %T: %w", value, err)
		}
		raw = encoded
	}

	raw = []byte(strings.TrimSpace(string(raw)))
	if !json.Valid(raw) {
		decoded, err := base64.StdEncoding.DecodeString(string(raw))
		if err != nil {
			return vatAircraftData{}, fmt.Errorf("dataref value is neither JSON nor valid base64-encoded JSON: %w", err)
		}
		raw = decoded
	}

	var data vatAircraftData
	if err := json.Unmarshal(raw, &data); err != nil {
		return vatAircraftData{}, fmt.Errorf("parse JSON payload: %w", err)
	}
	if data.Planes == nil {
		return vatAircraftData{}, fmt.Errorf("JSON payload has no planes array")
	}
	return data, nil
}

func newVATAircraft(plane vatPlane, now time.Time, service *atc.Service) *atc.Aircraft {
	registration := fmt.Sprintf("VAT-%d", plane.PlaneID)
	aircraft := &atc.Aircraft{
		Registration: registration,
		Type:         plane.SpawnedAircraftICAO,
		Code:         plane.AircraftICAO,
		Flight: atc.Flight{
			Number: flightNumber(plane.FlightNumber),
			Squawk: "",
			Phase: flightphase.Phase{
				Class:      flightclass.Unknown,
				Current:    flightphase.Unknown.Index(),
				Previous:   flightphase.Unknown.Index(),
				Transition: now,
			},
			Comms: atc.Comms{Callsign: plane.FlightNumber},
		},
	}
	setVATAirline(aircraft, plane.OperatorName, service)
	return aircraft
}

func setVATAirline(aircraft *atc.Aircraft, operatorCode string, service *atc.Service) {
	if operatorCode == "" {
		return
	}
	airline := service.GetAirlineByCode(operatorCode)
	if airline == nil {
		return
	}
	aircraft.Flight.Airline = airline
	aircraft.Flight.Comms.CountryCode = airline.CountryCode
}

func flightNumber(callsign string) int {
	if strings.TrimSpace(callsign) == "" {
		return 0
	}

	for i := len(callsign) - 1; i >= 0; i-- {
		if callsign[i] < '0' || callsign[i] > '9' {
			if i == len(callsign)-1 {
				return 0
			}
			number, err := strconv.Atoi(callsign[i+1:])
			if err != nil {
				logger.Log.Warnf("could not parse numeric suffix from VAT flight number %q: %v", callsign, err)
				return 0
			}
			return number
		}
	}
	number, err := strconv.Atoi(callsign)
	if err != nil {
		logger.Log.Warnf("could not parse VAT flight number %q: %v", callsign, err)
		return 0
	}
	return number
}

func vatFlightPhase(state string, previousPhase int) (flightphase.FlightPhase, bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "static", "waiting":
		return flightphase.Parked, true
	case "taxiing":
		switch flightphase.FlightPhase(previousPhase) {
		case flightphase.Cruise, flightphase.Arrival, flightphase.Holding,
			flightphase.Approach, flightphase.Final, flightphase.GoAround,
			flightphase.Braking, flightphase.TaxiIn:
			return flightphase.TaxiIn, true
		default:
			return flightphase.TaxiOut, true
		}
	case "taking off":
		return flightphase.Takeoff, true
	case "cruising":
		return flightphase.Cruise, true
	case "landing":
		return flightphase.Final, true
	default:
		return flightphase.Unknown, false
	}
}

func getDatarefByName(datarefs map[int]*xpapimodel.Dataref, name string) *xpapimodel.Dataref {
	for _, dataref := range datarefs {
		if dataref != nil && dataref.Name == name {
			return dataref
		}
	}
	return nil
}
