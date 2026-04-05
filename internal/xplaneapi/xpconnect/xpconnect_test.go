package xpconnect

import (
	"fmt"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
)

type MockATC struct {
	atc.Service
	NotifyCount           int
	ReceivedPreviousPhase int // New field to capture the state
}

func (m *MockATC) NotifyFlightPhaseChange(ac *atc.Aircraft) {
	m.NotifyCount++
	// Capture what the "Previous" phase was at the moment the service was called
	m.ReceivedPreviousPhase = ac.Flight.Phase.Previous
}

// Implement other interface methods as NOPs
func (m *MockATC) SetSimTime(t1, t2 time.Time)                         {}
func (m *MockATC) GetAirline(c string) *atc.AirlineInfo                { return nil }
func (m *MockATC) GetUserState() atc.UserState                         { return atc.UserState{} }
func (m *MockATC) GetWeatherState() *atc.Weather                       { return &atc.Weather{} }
func (m *MockATC) NotifyUserChange(p atc.Position, f1, f2 map[int]int) {}
func (m *MockATC) AddFlightPlan(ac *atc.Aircraft, t time.Time) bool    { return false }
func (m *MockATC) GetCurrentZuluTime() time.Time                       { return time.Now() }
func (m *MockATC) SetDataProvider(dp simdata.SimDataProvider)          {}

func setupMockDatarefs(tail string, flightNum int, phase int) map[int]*xpapimodel.Dataref {

	simdata.DRTrafficEngineAIPositionLat     = "trafficglobal/ai/position_lat"
	simdata.DRTrafficEngineAIPositionLong    = "trafficglobal/ai/position_long"
	simdata.DRTrafficEngineAIPositionHeading = "trafficglobal/ai/position_heading"
	simdata.DRTrafficEngineAIPositionElev    = "trafficglobal/ai/position_elev"
	simdata.DRTrafficEngineAIAircraftCode    = "trafficglobal/ai/aircraft_code"
	simdata.DRTrafficEngineAIAirlineCode     = "trafficglobal/ai/airline_code"
	simdata.DRTrafficEngineAITailNumber      = "trafficglobal/ai/tail_number"
	simdata.DRTrafficEngineAIClass           = "trafficglobal/ai/ai_class"
	simdata.DRTrafficEngineAIFlightNum       = "trafficglobal/ai/flight_num"
	simdata.DRTrafficEngineAIParking         = "trafficglobal/ai/parking"
	simdata.DRTrafficEngineAIFlightPhase     = "trafficglobal/ai/flight_phase"
	simdata.DRTrafficEngineAIRunway          = "trafficglobal/ai/runway"

	m := make(map[int]*xpapimodel.Dataref)

	// Essential Keys
	m[1] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAITailNumber, Value: []string{tail}, DecodedDataType: "base64_string_array"}
	m[2] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIFlightNum, Value: []int{flightNum}, DecodedDataType: "int_array"}
	m[3] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIFlightPhase, Value: []int{phase}, DecodedDataType: "int_array"}

	// Mock airline codes so airlineCodes[index] doesn't panic
	m[11] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIAirlineCode, Value: []string{"BAW"}, DecodedDataType: "base64_string_array"}

	// Position Data (prevents nil pointer panics during assignment)
	m[4] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIPositionLat, Value: []float64{51.15}, DecodedDataType: "float_array"}
	m[5] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIPositionLong, Value: []float64{-0.17}, DecodedDataType: "float_array"}
	m[6] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIPositionElev, Value: []float64{195.0}, DecodedDataType: "float_array"}
	m[7] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIPositionHeading, Value: []float64{347.0}, DecodedDataType: "float_array"}

	// Class and Assignment Data
	m[8] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIClass, Value: []int{3}, DecodedDataType: "int_array"}
	m[9] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIParking, Value: []string{"Gate A1"}, DecodedDataType: "base64_string_array"}
	m[10] = &xpapimodel.Dataref{Name: simdata.DRTrafficEngineAIRunway, Value: []string{"26L"}, DecodedDataType: "base64_string_array"}

	return m
}

func TestAircraftStateTransition(t *testing.T) {
	mockATC := &MockATC{}
	xpc := &XPConnect{
		aircraftMap:                 make(map[string]*atc.Aircraft),
		atcService:                  mockATC,
		initialised:                 true,
		memSubscribeDataRefIndexMap: setupMockDatarefs("G-CLPE", 2731, 1), // Phase 1 = Parked
	}

	// EXECUTION
	fmt.Println("Simulating 5 consecutive data updates...")
	for i := 0; i < 5; i++ {
		xpc.updateAircraftData()
	}

	// VERIFICATION
	if mockATC.NotifyCount > 1 {
		t.Errorf("FAIL: NotifyAircraftChange called %d times. Expected: 1", mockATC.NotifyCount)
	} else if mockATC.NotifyCount == 1 {
		t.Log("SUCCESS: Transition handled exactly once.")
	} else {
		t.Error("FAIL: Notification never triggered.")
	}
}

func TestUnknownTransitionPreserved(t *testing.T) {
	mockATC := &MockATC{}
	xpc := &XPConnect{
		aircraftMap:                 make(map[string]*atc.Aircraft),
		atcService:                  mockATC,
		initialised:                 true,
		memSubscribeDataRefIndexMap: setupMockDatarefs("G-CLPE", 2731, 1),
	}

	xpc.updateAircraftData()

	// In xpconnect_test.go
	expectedUnknown := int(flightphase.Unknown.Index()) // This should be -1 based on your fail

	if mockATC.ReceivedPreviousPhase != expectedUnknown {
		t.Errorf("Logic Error: ATC service saw Previous Phase as %d, expected %d",
			mockATC.ReceivedPreviousPhase, expectedUnknown)
	}
}
