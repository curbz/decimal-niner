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
    datarefIDs = make(map[string]int64)
    nextID int64 = 1000
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
        if name == "trafficglobal/ai/position_lat" || name == "trafficglobal/ai/position_long" || name == "trafficglobal/ai/position_elev" {
            vt = "float[]"
        } else if name == "trafficglobal/ai/ai_type" || name == "trafficglobal/ai/ai_class" || name == "trafficglobal/ai/flight_num" {
            vt = "int[]"
        }
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
                    Datarefs []struct{ Id int64 `json:"id"` } `json:"datarefs"`
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
                    payload := make(map[string]string)
                    for _, id := range ids {
                        // For binary-like datarefs produce base64 null-terminated strings
                        sample := []byte("CODE1\x00CODE2\x00CODE3\x00")
                        // vary sample slightly per iteration
                        sample = []byte(fmt.Sprintf("A%02d_1\x00A%02d_2\x00A%02d_3\x00", i, i, i))
                        payload[strconv.FormatInt(id, 10)] = base64.StdEncoding.EncodeToString(sample)
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
