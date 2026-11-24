package mockserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/curbz/decimal-niner/internal/simdata"
)

func TestVATDatarefsAreServedAndRefreshIntervalCanBeWritten(t *testing.T) {
	query := url.Values{}
	query.Add("filter[name]", simdata.DRVATPlanesJSON)
	query.Add("filter[name]", simdata.DRVATPlanesFrequencySeconds)

	request := httptest.NewRequest(http.MethodGet, "/api/v2/datarefs?"+query.Encode(), nil)
	recorder := httptest.NewRecorder()
	datarefsDispatcher(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("datarefs request status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var response struct {
		Data []DatarefInfo `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode datarefs response: %v", err)
	}
	ids := make(map[string]int64, len(response.Data))
	for _, dataref := range response.Data {
		ids[dataref.Name] = dataref.ID
		if dataref.Name == simdata.DRVATPlanesFrequencySeconds && !dataref.IsWritable {
			t.Fatal("VAT refresh-frequency dataref is not writable")
		}
	}
	if len(ids) != 2 {
		t.Fatalf("returned %d VAT datarefs, want 2", len(ids))
	}

	aircraftRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v2/datarefs/"+strconv.FormatInt(ids[simdata.DRVATPlanesJSON], 10)+"/value",
		nil,
	)
	aircraftRecorder := httptest.NewRecorder()
	datarefsDispatcher(aircraftRecorder, aircraftRequest)
	if aircraftRecorder.Code != http.StatusOK {
		t.Fatalf("aircraft dataref status = %d, want %d", aircraftRecorder.Code, http.StatusOK)
	}
	var aircraftResponse struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(aircraftRecorder.Body.Bytes(), &aircraftResponse); err != nil {
		t.Fatalf("decode aircraft dataref response: %v", err)
	}
	aircraftJSON, err := base64.StdEncoding.DecodeString(aircraftResponse.Data)
	if err != nil {
		t.Fatalf("decode base64 aircraft snapshot: %v", err)
	}
	var snapshot struct {
		SchemaVersion int `json:"schemaVersion"`
		PlaneCount    int `json:"planeCount"`
		Planes        []struct {
			PlaneID      int    `json:"planeID"`
			FlightNumber string `json:"flightNumber"`
			State        string `json:"state"`
		} `json:"planes"`
	}
	if err := json.Unmarshal(aircraftJSON, &snapshot); err != nil {
		t.Fatalf("decode aircraft snapshot JSON: %v", err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.PlaneCount != 3 || len(snapshot.Planes) != 3 {
		t.Fatalf("unexpected aircraft snapshot header: %+v", snapshot)
	}
	if snapshot.Planes[0].PlaneID != 21 || snapshot.Planes[0].FlightNumber == "" || snapshot.Planes[0].State == "" {
		t.Fatalf("unexpected sample plane: %+v", snapshot.Planes[0])
	}

	patchRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v2/datarefs/"+strconv.FormatInt(ids[simdata.DRVATPlanesFrequencySeconds], 10)+"/value",
		strings.NewReader(`{"data":5}`),
	)
	patchRecorder := httptest.NewRecorder()
	datarefsDispatcher(patchRecorder, patchRequest)
	if patchRecorder.Code != http.StatusOK {
		t.Fatalf("refresh-frequency PATCH status = %d, want %d", patchRecorder.Code, http.StatusOK)
	}

	frequencyRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v2/datarefs/"+strconv.FormatInt(ids[simdata.DRVATPlanesFrequencySeconds], 10)+"/value",
		nil,
	)
	frequencyRecorder := httptest.NewRecorder()
	datarefsDispatcher(frequencyRecorder, frequencyRequest)
	var frequencyResponse struct {
		Data float64 `json:"data"`
	}
	if err := json.Unmarshal(frequencyRecorder.Body.Bytes(), &frequencyResponse); err != nil {
		t.Fatalf("decode refresh-frequency response: %v", err)
	}
	if frequencyResponse.Data != 5 {
		t.Errorf("refresh interval = %v, want 5", frequencyResponse.Data)
	}
}

func TestVATMockCyclesThroughFlightPhases(t *testing.T) {
	wantStates := []string{"Static", "Taxiing", "Taking Off", "Cruising", "Landing", "Taxiing", "Finished"}
	for iter, want := range wantStates {
		encoded, ok := samplePayloadForName(simdata.DRVATPlanesJSON, "binary[]", iter).(string)
		if !ok {
			t.Fatalf("iteration %d returned a non-string VAT dataref", iter)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("iteration %d returned invalid base64: %v", iter, err)
		}
		var snapshot struct {
			PlaneCount int `json:"planeCount"`
			Planes     []struct {
				PlaneID int    `json:"planeID"`
				State   string `json:"state"`
			} `json:"planes"`
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			t.Fatalf("iteration %d returned invalid snapshot JSON: %v", iter, err)
		}
		if snapshot.PlaneCount != 3 || len(snapshot.Planes) != snapshot.PlaneCount {
			t.Fatalf("iteration %d has %d planes, declared %d", iter, len(snapshot.Planes), snapshot.PlaneCount)
		}
		if got := snapshot.Planes[0].State; got != want {
			t.Errorf("iteration %d state = %q, want %q", iter, got, want)
		}
	}
}
