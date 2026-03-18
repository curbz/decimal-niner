package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/mockserver"
	"github.com/curbz/decimal-niner/internal/trafficglobal"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpconnect"
	"github.com/curbz/decimal-niner/pkg/util"
)

type d9config struct {
	D9 struct {
		LoggingLevel string `yaml:"logging_level"`
	} `yaml:"d9"`
}

func main() {

	configFlag := flag.String("config", "", "Path to the config file (optional)")

	// mock server to emulate X-Plane REST+WebSocket
	mock := flag.Bool("mock", false, "start mock X-Plane server locally")

	flag.Parse()

	var cfgPath string

	// logic to determine which path to use
	if *configFlag != "" {
		// If user provided a path, use it directly
		cfgPath = *configFlag
	} else {
		// Check for custom config file location
		cfgPath = os.Getenv("D9_CONFIG_PATH")
		if cfgPath == "" {
			cfgPath = "config.yaml"
		} else {
			log.Println("loading configuration from custom location", cfgPath)
		}
	}

	cfg, err := util.LoadConfig[d9config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}

	// Initialize the logger once at start
	logger.Init(cfg.D9.LoggingLevel)

	if *mock {
		logger.Log.Println("Starting local mock X-Plane server on :8086")
		srv := mockserver.Start("8086")
		defer srv.Close()
		// small pause to let mock server start before client attempts to connect
		time.Sleep(250 * time.Millisecond)
	}

	// Get flight schedules from traffic global
	tgConfig := trafficglobal.LoadConfig(cfgPath)
	fScheds, airports := trafficglobal.BGLReader(tgConfig.TG.BGLFile)

	// Create ATC service
	atcService := atc.New(cfgPath, fScheds, airports)
	atcService.Run()

	// Connect to X-Plane
	xpc := xpconnect.New(cfgPath, atcService)
	atcService.SetDataProvider(xpc)
	xpc.Start()

	// Wait for interrupt signal to gracefully shutdown
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// Keep application alive until interrupt
	logger.Log.Println("Press Ctrl+C to shutdown.")
	<-interrupt

	logger.Log.Println("Received interrupt, shutting down...")
	xpc.Stop()
}
