package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// RadarBlip represents the minimal telemetry data needed by the browser canvas
type RadarBlip struct {
	Callsign     string  `json:"callsign"`
	Registration string  `json:"registration"`
	Aircraft     string  `json:"ac_type"`
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	Altitude     float64 `json:"alt"`
	Heading      int     `json:"hdg"`
	Phase        string  `json:"phase"`
	Origin       string  `json:"origin"`
	Destination  string  `json:"dest"`
	GroundSpeed  float64 `json:"gs"`
}

// RadarSnapshot is the frame package sent on every tick
type RadarSnapshot struct {
	CenterLat float64     `json:"center_lat"` // The coordinate the scope should center on
	CenterLng float64     `json:"center_lng"`
	Timestamp time.Time   `json:"timestamp"`
	Aircraft  []RadarBlip `json:"aircraft"`
}

type RadarServer struct {
	sync.RWMutex
	clients map[chan RadarSnapshot]bool
}

func NewRadarServer() *RadarServer {
	return &RadarServer{
		clients: make(map[chan RadarSnapshot]bool),
	}
}

// ServeHTTP implements the http.Handler interface for streaming data
func (rs *RadarServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set headers required for Server-Sent Events
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*") // Adjust for production safety

	// Create a channel for this specific browser session
	messageChan := make(chan RadarSnapshot, 10)

	rs.Lock()
	rs.clients[messageChan] = true
	rs.Unlock()

	// Ensure cleanup when the browser tab closes or disconnects
	defer func() {
		rs.Lock()
		delete(rs.clients, messageChan)
		close(messageChan)
		rs.Unlock()
	}()

	// Listen for closing connections or new data frames
	for {
		select {
		case snapshot := <-messageChan:
			jsonData, err := json.Marshal(snapshot)
			if err != nil {
				continue
			}
			// SSE format requires data: prefix followed by double newlines
			_, _ = w.Write([]byte("data: " + string(jsonData) + "\n\n"))
			w.(http.Flusher).Flush() // Push the data down the wire immediately

		case <-r.Context().Done():
			return
		}
	}
}

// BroadcastSnapshot is called by your D9TrafficEngine loop on every frame tick
func (rs *RadarServer) BroadcastSnapshot(snapshot RadarSnapshot) {
	rs.RLock()
	defer rs.RUnlock()

	for clientChan := range rs.clients {
		select {
		case clientChan <- snapshot:
		default:
			// Client buffer is full; drop frame to prevent lagging or stalling the main engine loop
		}
	}
}
