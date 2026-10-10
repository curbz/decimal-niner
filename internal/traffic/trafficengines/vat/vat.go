package vat

import (
	"github.com/curbz/decimal-niner/internal/atc"
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
}

func New(cfgPath string) (atc.TrafficEngine, error) {

	cfg, err := util.LoadConfig[VATconfig](cfgPath)
	if err != nil {
		logger.Log.Errorf("Error reading Virtual Air Traffic engine configuration file: %v", err)
		return nil, err
	}

	// TODO
	// add a mechanism to write to dataref virtualairtraffic/traffic/planes_freq_s with RefreshIntervalSeconds
	// this must happen BEFORE the planes_json dataref is subscribed to otherwise planes_json will not be updated

	simdata.DRVATAircraftJSON = "virtualairtraffic/traffic/planes_json"

	subscribeDatarefs := []xpapimodel.Dataref{
		{Name: simdata.DRVATAircraftJSON,
			APIInfo: xpapimodel.DatarefInfo{}, Value: nil, DecodedDataType: "json"},
	}
	simdata.SubscribeDatarefs = append(simdata.SubscribeDatarefs, subscribeDatarefs...)

	te := &VATTraffic{
		RefreshIntervalSeconds: cfg.VAT.RefreshIntervalSeconds,
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

func (e *VATTraffic) HandleAircraftData(datarefs map[int]*xpapimodel.Dataref) {
	// VAT provides a single JSON aircraft payload rather than the Traffic Global array layout.
	// This handler is intentionally isolated here so the new JSON format can be parsed without
	// coupling X-Plane connection code to a specific traffic engine.
	_ = datarefs
}
