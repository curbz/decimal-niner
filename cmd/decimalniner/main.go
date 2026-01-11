package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/mockserver"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpconnect"
)


func main() {

	configFlag := flag.String("config", "", "Path to the config file (optional)")
	
	// Support a development mock server to emulate X-Plane REST+WebSocket
	mock := flag.Bool("mock", false, "start mock X-Plane server locally")
	
	flag.Parse()

	var cfgPath string
	var err error

	// 2. Logic to determine which path to use
	if *configFlag != "" {
		// If user provided a path, use it directly
		cfgPath = *configFlag
	} else {
		// If not, search for "config.yaml" up to 2 levels up
		cfgPath, err = FindConfigFile("config.yaml")
		if err != nil {
			log.Printf("Error locating config: %v\n", err)
			os.Exit(1)
		}
	}

	if *mock {
		log.Println("Starting local mock X-Plane server on :8086")
		srv := mockserver.Start("8086")
		defer srv.Close()
		// small pause to let server start before client attempts to connect
		time.Sleep(150 * time.Millisecond)
	}

	// Create ATC service
	atcService := atc.New(cfgPath)
	atcService.Run()

	// Connect to X-Plane
	xpc := xpconnect.New(cfgPath, atcService)
	xpc.Start()

	// Wait for interrupt signal to gracefully shutdown
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	<-interrupt
	log.Println("Received interrupt, shutting down...")
	xpc.Stop()
}

// FindConfigFile searches for the file in the current dir and moves up 2 levels if not found.
func FindConfigFile(filename string) (string, error) {
	currDir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// We check current, parent, and grandparent (3 attempts total)
	for i := 0; i < 3; i++ {
		path := filepath.Join(currDir, filename)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		
		// Move up one level
		currDir = filepath.Dir(currDir)
	}

	return "", fmt.Errorf("config file %q not found in current or parent directories", filename)
}


