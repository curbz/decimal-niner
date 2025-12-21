package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/yourusername/decimal-niner/internal/mockserver"
	"github.com/yourusername/decimal-niner/pkg/xpconnect"
)

// --- Main Application ---

func main() {
	// Support a development mock server to emulate X-Plane REST+WebSocket
	mock := flag.Bool("mock", false, "start mock X-Plane server locally")
	flag.Parse()
	if *mock {
		log.Println("Starting local mock X-Plane server on :8086")
		srv := mockserver.Start("8086")
		defer srv.Close()
		// small pause to let server start before client attempts to connect
		time.Sleep(150 * time.Millisecond)
	}

	// Connect to X-Plane
	xpconnect.Start()

	// Wait for interrupt signal to gracefully shutdown
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	<-interrupt
	log.Println("Received interrupt, shutting down...")
	xpconnect.Stop()
}
