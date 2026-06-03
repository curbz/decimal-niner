package trafficglobal

import (
	"testing"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/flightphase"
	"github.com/curbz/decimal-niner/internal/traffic"
)

func newTestEngine(simTime time.Time) *TrafficGlobal {
	svc := &atc.Service{}
	svc.SyncSimTime(simTime, simTime)

	return &TrafficGlobal{
        CommonTrafficEngine: traffic.CommonTrafficEngine{
            AtcService: svc,
        },
	}
}

func TestCheckForCruiseSectorChange(t *testing.T) {
	tests := []struct {
		name                 string
		setup                func() (*atc.Service, *atc.Aircraft)
		wantLastCheckedDelta bool // whether LastCheckedPosition should be updated to Position
	}{
		{
			name: "cruise initial checkpoint sets last checked",
			setup: func() (*atc.Service, *atc.Aircraft) {
				s := &atc.Service{}
				ac := &atc.Aircraft{}
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.LastCheckedPosition = atc.Position{Lat: 0, Long: 0}
				ac.Flight.Position = atc.Position{Lat: 51.5, Long: -0.2}
				return s, ac
			},
			wantLastCheckedDelta: true,
		},
		{
			name: "cruise small movement - no update",
			setup: func() (*atc.Service, *atc.Aircraft) {
				s := &atc.Service{}
				ac := &atc.Aircraft{}
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.LastCheckedPosition = atc.Position{Lat: 51.0000, Long: -0.1000}
				ac.Flight.Position = atc.Position{Lat: 51.00005, Long: -0.10005} // ~ few meters
				ac.Flight.Comms.Controller = &atc.Controller{}
				ac.Flight.Comms.CruiseHandoff = atc.NoHandoff
				return s, ac
			},
			wantLastCheckedDelta: false,
		},
		{
			name: "cruise large movement - update last checked",
			setup: func() (*atc.Service, *atc.Aircraft) {
				s := &atc.Service{}
				ac := &atc.Aircraft{}
				ac.Flight.Phase.Current = flightphase.Cruise.Index()
				ac.Flight.LastCheckedPosition = atc.Position{Lat: 51.0, Long: -0.1}
				ac.Flight.Position = atc.Position{Lat: 51.2, Long: -0.1} // ~0.2 deg lat ~= 12 NM
				ac.Flight.Comms.Controller = &atc.Controller{}
				ac.Flight.Comms.CruiseHandoff = atc.NoHandoff
				return s, ac
			},
			wantLastCheckedDelta: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEngine(time.Now())
			_, ac := tc.setup()
			before := ac.Flight.LastCheckedPosition
			e.CheckForCruiseSectorChange(ac)
			after := ac.Flight.LastCheckedPosition
			if tc.wantLastCheckedDelta {
				if before.Lat == after.Lat && before.Long == after.Long {
					t.Fatalf("expected LastCheckedPosition to change but it did not: before=%v after=%v", before, after)
				}
			} else {
				if before.Lat != after.Lat || before.Long != after.Long {
					t.Fatalf("expected LastCheckedPosition to remain same but it changed: before=%v after=%v", before, after)
				}
			}

		})
	}
}