package mockserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type DatarefInfo struct {
	ID         int64  `json:"id"`
	IsWritable bool   `json:"is_writable"`
	Name       string `json:"name"`
	ValueType  string `json:"value_type"`
}

var (
	upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	mu       sync.Mutex
	// maintain deterministic IDs per dataref name
	datarefIDs       = make(map[string]int64)
	nextID     int64 = 1000
	// mappings for id -> name and id -> value type (set when /datarefs is queried)
	idToName      = make(map[int64]string)
	idToValueType = make(map[int64]string)
	// known datarefs and their canonical value types
	datarefDefs = map[string]string{

		"sim/flightmodel/position/latitude":  "double",
		"sim/flightmodel/position/longitude": "double",
		"sim/flightmodel/position/elevation": "double",
		"sim/flightmodel/position/psi":       "float",

		"sim/cockpit/radios/com1_freq_hz": "int",
		"sim/cockpit/radios/com2_freq_hz": "int",

		"sim/atc/com1_tuned_facility": "int",
		"sim/atc/com2_tuned_facility": "int",

		"sim/time/local_date_days": "int",
		"sim/time/local_time_sec":  "float",
		"sim/time/zulu_time_sec":   "float",

		"trafficglobal/ai/position_lat":     "float[]",
		"trafficglobal/ai/position_long":    "float[]",
		"trafficglobal/ai/position_heading": "float[]",
		"trafficglobal/ai/position_elev":    "float[]",

		"trafficglobal/ai/aircraft_code": "binary[]",
		"trafficglobal/ai/airline_code":  "binary[]",
		"trafficglobal/ai/tail_number":   "binary[]",

		"trafficglobal/ai/ai_type":    "int[]",
		"trafficglobal/ai/ai_class":   "int[]",
		"trafficglobal/ai/flight_num": "int[]",

		"trafficglobal/ai/parking":      "binary[]",
		"trafficglobal/ai/flight_phase": "int[]",
		"trafficglobal/ai/runway":       "int[]",
	}
)

func idFor(name string) int64 {
	mu.Lock()
	defer mu.Unlock()
	if id, ok := datarefIDs[name]; ok {
		return id
	}
	id := nextID
	nextID++
	datarefIDs[name] = id
	return id
}

// Start starts the mock HTTP + WebSocket server on the given port (e.g. "8086").
// It returns the *http.Server so the caller can shut it down when desired.
func Start(port string) *http.Server {
	mux := http.NewServeMux()
	// register both exact and subtree patterns so requests to
	// /api/v2/datarefs and /api/v2/datarefs/{id}/value are routed
	// to the dispatcher which will further route to the value
	// handler when the path ends with "/value".
	mux.HandleFunc("/api/v2/datarefs", datarefsDispatcher)
	mux.HandleFunc("/api/v2/datarefs/", datarefsDispatcher)
	mux.HandleFunc("/api/v2", wsHandler)

	srv := &http.Server{Addr: ":" + port, Handler: mux}
	go func() {
		log.Printf("mockserver: listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("mockserver: ListenAndServe error: %v", err)
		}
	}()
	return srv
}

func datarefsHandler(w http.ResponseWriter, r *http.Request) {
	// Collect requested filters
	q := r.URL.Query()["filter[name]"]
	// If none provided, return the sim time datarefs
	if len(q) == 0 {
		q = []string{"sim/time/local_date_days", "sim/time/local_time_sec", "sim/time/zulu_time_sec"}
	}

	data := make([]DatarefInfo, 0, len(q))
	for _, name := range q {
		id := idFor(name)
		vt := "binary[]"
		if v, ok := datarefDefs[name]; ok {
			vt = v
		}
		// record mappings for later WS payload generation
		mu.Lock()
		idToName[id] = name
		idToValueType[id] = vt
		mu.Unlock()

		data = append(data, DatarefInfo{ID: id, IsWritable: false, Name: name, ValueType: vt})
	}

	resp := map[string]interface{}{"data": data}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func datarefsDispatcher(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/value") {
		datarefValueHandler(w, r)
	} else {
		datarefsHandler(w, r)
	}
}

func datarefValueHandler(w http.ResponseWriter, r *http.Request) {
	// Path should be /api/v2/datarefs/{id}/value
	path := r.URL.Path
	if !strings.HasSuffix(path, "/value") {
		http.NotFound(w, r)
		return
	}
	// Extract id from /api/v2/datarefs/{id}/value
	parts := strings.Split(strings.TrimPrefix(path, "/api/v2/datarefs/"), "/")
	if len(parts) != 2 || parts[1] != "value" {
		http.NotFound(w, r)
		return
	}
	idStr := parts[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	mu.Lock()
	name := idToName[id]
	vt := idToValueType[id]
	mu.Unlock()

	if name == "" {
		http.NotFound(w, r)
		return
	}

	value := samplePayloadForName(name, vt, 0)

	resp := map[string]interface{}{"data": value}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("mockserver: websocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// read initial messages and react to subscription requests
	for {
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("mockserver: read error: %v", err)
			return
		}
		if mt != websocket.TextMessage {
			continue
		}

		var incoming map[string]json.RawMessage
		if err := json.Unmarshal(msg, &incoming); err != nil {
			log.Printf("mockserver: invalid JSON: %v", err)
			continue
		}

		// Inspect "type" field
		var t string
		if v, ok := incoming["type"]; ok {
			json.Unmarshal(v, &t)
		}

		switch t {
		case "dataref_subscribe_values":
			// respond with a success result and then send an update
			var req struct {
				ReqID int64 `json:"req_id"`
			}
			json.Unmarshal(incoming["req_id"], &req.ReqID)

			// send result
			result := map[string]interface{}{"req_id": req.ReqID, "type": "result", "success": true}
			conn.WriteJSON(result)

			// Find subscribed ids (params.datarefs[].id)
			var params struct {
				Params struct {
					Datarefs []struct {
						Id int64 `json:"id"`
					} `json:"datarefs"`
				} `json:"params"`
			}
			json.Unmarshal(msg, &params)

			ids := make([]int64, 0, len(params.Params.Datarefs))
			for _, d := range params.Params.Datarefs {
				ids = append(ids, d.Id)
			}

			// send a few updates asynchronously
			go func(ids []int64) {
				for i := 0; i < 3; i++ {
					time.Sleep(750 * time.Millisecond)
					payload := make(map[string]interface{})
					for _, id := range ids {
						mu.Lock()
						vt := idToValueType[id]
						mu.Unlock()

						// Prefer name-specific samples when available
						name := ""
						mu.Lock()
						name = idToName[id]
						mu.Unlock()

						payload[strconv.FormatInt(id, 10)] = samplePayloadForName(name, vt, i)
					}
					msg := map[string]interface{}{"type": "dataref_update_values", "data": payload}
					conn.WriteJSON(msg)
				}
			}(ids)

		default:
			// echo unknown messages
			log.Printf("mockserver: received unknown ws type=%q msg=%s", t, string(msg))
		}
	}
}

// samplePayloadForName returns an appropriate sample payload for the given
// dataref name and value type. The returned value is JSON-serializable and
// matches what the client expects for that type (e.g., numeric arrays or
// base64-encoded binary strings).
func samplePayloadForName(name, vt string, iter int) interface{} {
	switch name {
	// --- User Position (Heathrow Center) ---
	case "sim/flightmodel/position/latitude":
		return 51.4700 + (float64(iter) * 0.0001)
	case "sim/flightmodel/position/longitude":
		return -0.4543 + (float64(iter) * 0.0001)
	case "sim/flightmodel/position/elevation":
		return 25.0 + float64(iter) // EGLL is ~80ft MSL
	case "sim/flightmodel/position/psi":
		return 270.5 // Facing West towards Runway 27R

	// --- User Radios (Heathrow Frequencies) ---
	case "sim/cockpit/radios/com1_freq_hz":
		return int(11850) // EGLL Tower
	case "sim/cockpit/radios/com2_freq_hz":
		return int(12190) // EGLL Ground
	case "sim/atc/com1_tuned_facility":
		return 3 // Tower
	case "sim/atc/com2_tuned_facility":
		return 2 // Ground

	// --- Sim Time ---
	case "sim/time/local_date_days":
		return 15 // Example: days since Jan 1
	case "sim/time/local_time_sec":
		return 39600.0 + float64(iter) // 11:00:00 am local time
	case "sim/time/zulu_time_sec":
		return 39600.0 + float64(iter) // 12:00:00 Zulu

	// --- AI Aircraft Data (Moving around EGLL) ---
	case "trafficglobal/ai/position_lat":
		return []float64{
			51.4695,                           // AC1: Near Terminal 5
			51.4710 + (float64(iter) * 0.001), // AC2: Taxiing toward 27R
			51.4770 + (float64(iter) * 0.005), // AC3: On Final Approach
		}

	case "trafficglobal/ai/position_long":
		return []float64{
			-0.4870,
			-0.4600 + (float64(iter) * 0.001),
			-0.3500 + (float64(iter) * 0.005),
		}

	case "trafficglobal/ai/position_heading":
		return []float64{90.0, 270.0, 270.0}

	case "trafficglobal/ai/position_elev":
		return []float64{
			25.0,  // Ground
			25.0,  // Ground
			300.5, // Descending on Final
		}

	case "trafficglobal/ai/aircraft_code":
		// A320, B738, A359
		s := "A320\x00B738\x00A359\x00"
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/ai/airline_code":
		s := "BAW\x00EZY\x00BAW\x00" // British Airways and EasyJet
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/ai/flight_phase":
		return []int{
			10 + iter,
			1 + iter,
			4 + iter,
		}

	case "trafficglobal/ai/runway":
		return []float64{4994866, 5388082, 5388082, 5388082}

	case "trafficglobal/ai/tail_number":
		// G-AOWK,281,EGLL,KLAX,4,10,25,4,21,45,154 <-- departure
		// G-ARBD,343,LFMN,EGLL,4,9,45,4,12,0,289   <-- arrival
		// G-BCOL,700,EGLL,LOWW,4,9,0,4,11,30,309   <-- departure

		s := "G-AOWK\x00G-ARBD\x00G-BCOL\x00"
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/ai/flight_num":
		return []int{281, 343, 700}

	case "trafficglobal/ai/parking":
		s := "GATE 22\x00GATE 5\x00RAMP 9\x00"
		return base64.StdEncoding.EncodeToString([]byte(s))
	}

	// Fallback based on declared value type
	switch vt {
	case "float[]":
		return []float64{1.1 + float64(iter), 2.2 + float64(iter)}
	case "int[]":
		return []int{1 + iter, 2 + iter, 3 + iter}
	default:
		s := fmt.Sprintf("VAL%02d\x00VAL%02d\x00", iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))
	}
}
