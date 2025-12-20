package mockserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
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

		// Some implementations return ICAO as both binary strings and ints; use binary[] for mock
		"trafficglobal/ai/source_icao": "binary[]",
		"trafficglobal/ai/dest_icao":   "binary[]",

		"trafficglobal/ai/parking":      "binary[]",
		"trafficglobal/ai/flight_phase": "int[]",
		"trafficglobal/ai/runway":       "int[]",
		"trafficglobal/ai/taxi_route":   "binary[]",
		"trafficglobal/airport_flows":   "binary[]",
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
	mux.HandleFunc("/api/v2/datarefs", datarefsHandler)
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
	// If none provided, return a small default set
	if len(q) == 0 {
		q = []string{"trafficglobal/ai/aircraft_code"}
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
	case "trafficglobal/ai/position_lat":
		return []float64{35.145877838134766 + float64(iter), 35.1459 + float64(iter), 35.146 + float64(iter)}
	case "trafficglobal/ai/position_long":
		return []float64{24.120702743530273 + float64(iter), 24.121 + float64(iter), 24.122 + float64(iter)}
	case "trafficglobal/ai/position_heading":
		return []float64{180.0 + float64(iter), 181.0 + float64(iter), 182.0 + float64(iter)}
	case "trafficglobal/ai/position_elev":
		return []float64{10372.2021484375 + float64(iter), 10380.0 + float64(iter), 10390.0 + float64(iter)}

	case "trafficglobal/ai/aircraft_code":
		s := fmt.Sprintf("AC%02d\x00BC%02d\x00CC%02d\x00", iter, iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))
	case "trafficglobal/ai/airline_code":
		s := fmt.Sprintf("AL%02d\x00BL%02d\x00CL%02d\x00", iter, iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))
	case "trafficglobal/ai/tail_number":
		s := fmt.Sprintf("TN%02d\x00TN%02d\x00TN%02d\x00", iter, iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/ai/source_icao":
		s := fmt.Sprintf("SRC%02d\x00SRC%02d\x00", iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))
	case "trafficglobal/ai/dest_icao":
		s := fmt.Sprintf("DST%02d\x00DST%02d\x00", iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/ai/parking":
		s := fmt.Sprintf("RAMP %d\x00APRON %d\x00", iter, iter)
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/ai/ai_type":
		return []int{0 + iter, 0 + iter, 1 + iter}
	case "trafficglobal/ai/ai_class":
		return []int{2, 2, 2}
	case "trafficglobal/ai/flight_num":
		return []int{471 + iter, 472 + iter, 473 + iter}
	case "trafficglobal/ai/flight_phase":
		return []int{5, 5, 5}

	case "trafficglobal/ai/runway":
		return []int{538756, 13107, 0, 0}

	case "trafficglobal/ai/taxi_route":
		// empty string when no taxiing
		s := ""
		return base64.StdEncoding.EncodeToString([]byte(s))

	case "trafficglobal/airport_flows":
		// return a short binary blob encoded as base64
		return base64.StdEncoding.EncodeToString([]byte{0x0b, 0x00, 0x01})
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
