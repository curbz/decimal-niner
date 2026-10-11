package vat

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/simdata"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpapimodel"
)

const sampleAircraftJSON = `{
	"schemaVersion": 1,
	"planeCount": 1,
	"planes": [{
		"planeID": 21,
		"modeSId": 500405,
		"flightNumber": "U28391",
		"operatorName": "EZY",
		"aircraftICAO": "A320",
		"spawnedAircraftICAO": "A319",
		"originICAO": "EGKK",
		"destinationICAO": "LFMT",
		"state": "Cruising",
		"latitude": 51.153,
		"longitude": -0.167,
		"altitudeFt": 35000,
		"speedKt": 430,
		"headingDeg": 114.5,
		"cruiseAltitudeFt": 35000
	}]
}`

func TestHandleAircraftDataPopulatesAndUpdatesAircraft(t *testing.T) {
	simdata.DRVATAircraftJSON = "virtualairtraffic/traffic/planes_json"
	engine := &VATTraffic{AircraftMap: make(map[int]*atc.Aircraft)}
	service := &atc.Service{}
	service.SyncSimTime(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC), time.Now())
	engine.SetATCService(service)

	engine.HandleAircraftData(map[int]*xpapimodel.Dataref{
		1: {Name: simdata.DRVATAircraftJSON, Value: sampleAircraftJSON},
	})

	aircraft, exists := engine.AircraftMap[21]
	if !exists {
		t.Fatal("expected VAT aircraft to be created")
	}
	if aircraft.Registration != "VAT-21" {
		t.Errorf("Registration = %q, want VAT-21", aircraft.Registration)
	}
	if aircraft.Type != "A319" || aircraft.Code != "A320" {
		t.Errorf("aircraft type/code = %q/%q, want A319/A320", aircraft.Type, aircraft.Code)
	}
	if aircraft.Flight.Number != 28391 || aircraft.Flight.Comms.Callsign != "U28391" {
		t.Errorf("flight identity = %d/%q, want 28391/U28391", aircraft.Flight.Number, aircraft.Flight.Comms.Callsign)
	}
	if aircraft.Flight.Origin != "EGKK" || aircraft.Flight.Destination != "LFMT" {
		t.Errorf("route = %s-%s, want EGKK-LFMT", aircraft.Flight.Origin, aircraft.Flight.Destination)
	}
	if aircraft.Flight.Position.Lat != 51.153 || aircraft.Flight.Position.Long != -0.167 ||
		aircraft.Flight.Position.Altitude != 35000 || aircraft.Flight.Position.Heading != 114.5 {
		t.Errorf("unexpected position: %+v", aircraft.Flight.Position)
	}
	if aircraft.Flight.GroundSpeed != 430 || aircraft.Flight.CruiseAlt != 35000 {
		t.Errorf("speed/cruise altitude = %v/%d, want 430/35000", aircraft.Flight.GroundSpeed, aircraft.Flight.CruiseAlt)
	}
	if aircraft.Flight.Phase.Current != flightphase.Cruise.Index() ||
		aircraft.Flight.Phase.Previous != flightphase.Cruise.Index() {
		t.Errorf("initial phase current/previous = %d/%d, want Cruise", aircraft.Flight.Phase.Current, aircraft.Flight.Phase.Previous)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(sampleAircraftJSON), &payload); err != nil {
		t.Fatal(err)
	}
	planes := payload["planes"].([]any)
	planes[0].(map[string]any)["state"] = "Taking Off"
	planes[0].(map[string]any)["latitude"] = 51.2

	engine.HandleAircraftData(map[int]*xpapimodel.Dataref{
		1: {Name: simdata.DRVATAircraftJSON, Value: payload},
	})

	if got := engine.AircraftMap[21]; got != aircraft {
		t.Fatal("expected subsequent snapshots to update the existing aircraft")
	}
	if aircraft.Flight.Position.Lat != 51.2 {
		t.Errorf("updated latitude = %v, want 51.2", aircraft.Flight.Position.Lat)
	}
	if aircraft.Flight.Phase.Current != flightphase.Takeoff.Index() ||
		aircraft.Flight.Phase.Previous != flightphase.Takeoff.Index() {
		t.Errorf("updated phase current/previous = %d/%d, want Takeoff", aircraft.Flight.Phase.Current, aircraft.Flight.Phase.Previous)
	}

	engine.HandleAircraftData(map[int]*xpapimodel.Dataref{
		1: {Name: simdata.DRVATAircraftJSON, Value: `{"schemaVersion":1,"planeCount":0,"planes":[]}`},
	})
	if len(engine.AircraftMap) != 0 {
		t.Errorf("expected aircraft absent from full snapshot to be removed, got %d", len(engine.AircraftMap))
	}
}

func TestDatarefsToWriteConfiguresVATRefreshInterval(t *testing.T) {
	engine := &VATTraffic{RefreshIntervalSeconds: 5}
	datarefs := engine.DatarefsToWrite()
	if len(datarefs) != 1 {
		t.Fatalf("DatarefsToWrite returned %d refs, want 1", len(datarefs))
	}
	if datarefs[0].Name != "virtualairtraffic/traffic/planes_freq_s" {
		t.Errorf("dataref name = %q, want virtualairtraffic/traffic/planes_freq_s", datarefs[0].Name)
	}
	if datarefs[0].Value != float64(5) {
		t.Errorf("dataref value = %v, want 5", datarefs[0].Value)
	}
}

func TestDecodeVATAircraftDataAcceptsBase64EncodedJSON(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(sampleAircraftJSON))
	got, err := decodeVATAircraftData(encoded)
	if err != nil {
		t.Fatalf("decodeVATAircraftData rejected base64 JSON: %v", err)
	}
	if got.SchemaVersion != 1 || got.PlaneCount != 1 || len(got.Planes) != 1 {
		t.Fatalf("decoded snapshot header = version %d, count %d, planes %d; want 1/1/1",
			got.SchemaVersion, got.PlaneCount, len(got.Planes))
	}
	if got.Planes[0].PlaneID != 21 || got.Planes[0].FlightNumber != "U28391" {
		t.Errorf("decoded plane = %+v, want planeID 21 and flight U28391", got.Planes[0])
	}
}

func TestDecodeVATAircraftDataRejectsMalformedOrInconsistentData(t *testing.T) {
	tests := []struct{ name, payload string }{
		{name: "malformed JSON", payload: `{"schemaVersion":`},
		{name: "missing planes", payload: `{"schemaVersion":1,"planeCount":0}`},
		{name: "trailing data", payload: `{"schemaVersion":1,"planeCount":0,"planes":[]} trailing`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeVATAircraftData(tt.payload); err == nil {
				t.Fatal("expected invalid VAT aircraft payload to be rejected")
			}
		})
	}
}

func TestHandleAircraftDataRejectsInconsistentPlaneCount(t *testing.T) {
	simdata.DRVATAircraftJSON = "virtualairtraffic/traffic/planes_json"
	engine := &VATTraffic{AircraftMap: make(map[int]*atc.Aircraft)}
	engine.SetATCService(&atc.Service{})

	engine.HandleAircraftData(map[int]*xpapimodel.Dataref{
		1: {
			Name:  simdata.DRVATAircraftJSON,
			Value: `{"schemaVersion":1,"planeCount":1,"planes":[]}`,
		},
	})

	if len(engine.AircraftMap) != 0 {
		t.Fatalf("expected inconsistent snapshot to be ignored, got %d aircraft", len(engine.AircraftMap))
	}
}

func TestHandleAircraftDataRejectsDuplicatePlaneIDs(t *testing.T) {
	simdata.DRVATAircraftJSON = "virtualairtraffic/traffic/planes_json"
	engine := &VATTraffic{AircraftMap: map[int]*atc.Aircraft{21: {Registration: "existing"}}}
	engine.SetATCService(&atc.Service{})
	engine.HandleAircraftData(map[int]*xpapimodel.Dataref{
		1: {
			Name: simdata.DRVATAircraftJSON,
			Value: `{"schemaVersion":1,"planeCount":2,"planes":[
				{"planeID":21,"state":"Cruising"},
				{"planeID":21,"state":"Taxiing"}
			]}`,
		},
	})
	if len(engine.AircraftMap) != 1 || engine.AircraftMap[21].Registration != "existing" {
		t.Fatal("expected duplicate-ID payload to be rejected without changing current aircraft")
	}
}

func TestVATFlightPhase(t *testing.T) {
	tests := []struct {
		state          string
		previous       flightphase.FlightPhase
		want           flightphase.FlightPhase
		wantRecognized bool
	}{
		{state: "Static", want: flightphase.Parked, wantRecognized: true},
		{state: "Waiting", want: flightphase.Parked, wantRecognized: true},
		{state: "Taxiing", want: flightphase.TaxiOut, wantRecognized: true},
		{state: "Taxiing", previous: flightphase.Approach, want: flightphase.TaxiIn, wantRecognized: true},
		{state: "Taking Off", want: flightphase.Takeoff, wantRecognized: true},
		{state: "Cruising", want: flightphase.Cruise, wantRecognized: true},
		{state: "Unexpected", want: flightphase.Unknown, wantRecognized: false},
	}
	for _, tt := range tests {
		got, recognized := vatFlightPhase(tt.state, tt.previous.Index())
		if got != tt.want || recognized != tt.wantRecognized {
			t.Errorf("vatFlightPhase(%q, %v) = %v, %v; want %v, %v",
				tt.state, tt.previous, got, recognized, tt.want, tt.wantRecognized)
		}
	}
}

func TestFlightNumberHandlesMissingCallsign(t *testing.T) {
	for _, callsign := range []string{"", " ", "\t"} {
		if got := flightNumber(callsign); got != 0 {
			t.Errorf("flightNumber(%q) = %d, want 0", callsign, got)
		}
	}
}

func TestEnrichSkipsWhenVATRunwayDataIsUnavailable(t *testing.T) {
	engine := &VATTraffic{}
	aircraft := &atc.Aircraft{
		Flight: atc.Flight{Phase: flightphase.Phase{Current: flightphase.TaxiOut.Index()}},
	}
	engine.Enrich(aircraft, &atc.Airport{})
	engine.Enrich(aircraft, nil)
	engine.Enrich(nil, &atc.Airport{})
}
