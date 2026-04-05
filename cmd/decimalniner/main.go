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
	"github.com/curbz/decimal-niner/internal/traffic"
	"github.com/curbz/decimal-niner/internal/trafficglobal"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpconnect"
	"github.com/curbz/decimal-niner/pkg/util"
)

type d9config struct {
	D9 struct {
		LoggingLevel string `yaml:"logging_level"`
		TrafficEngine string `yaml:"traffic_engine"`
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

	var srv any
	if *mock {
		if logger.Log != nil {
			logger.Log.Info("Starting local mock X-Plane server on :8086")
		}
		s := mockserver.Start("8086")
		srv = s
		if srv != nil {
			defer func() {
				switch v := srv.(type) {
				case interface{ Close() error }:
					if err := v.Close(); err != nil {
						if logger.Log != nil {
							logger.Log.Infof("error closing mock server: %v", err)
						}
					}
				case interface{ Close() }:
					v.Close()
				default:
					// no close available
				}
			}()
		}
		// small pause to let mock server start before client attempts to connect
		time.Sleep(250 * time.Millisecond)
	}

	var te traffic.Engine
	var teErr error
	switch cfg.D9.TrafficEngine {
	case "trafficglobal":
		te, teErr = trafficglobal.New(cfgPath)
	default:
		if logger.Log != nil {
			logger.Log.Fatalf("unsupported traffic engine specified in decimal-niner configuration: %s", cfg.D9.TrafficEngine)
		} 
		return
	}
	if teErr != nil {
		if logger.Log != nil {
			logger.Log.Fatalf("error initialising traffic engine: %v", err)
		}
		return
	}

	// Get flight schedules
	fScheds, airports := te.LoadFlightPlans(te.GetFlightPlanPath())

	// Create ATC service
	atcService, err := atc.New(cfgPath, fScheds, airports)
	if err != nil {
		if logger.Log != nil {
			logger.Log.Info("failed to create ATC service, exiting")
		}
		return
	}

	// set the airport service provider
	atcService.AirportService = atcService
	
	atcService.Run()

	// Connect to X-Plane
	xpc := xpconnect.New(cfgPath, atcService)
	atcService.SetDataProvider(xpc)
	if xpc == nil {
		logger.Log.Fatal("failed to create xpconnect")
	}

	xpc.Start()

	// Wait for interrupt signal to gracefully shutdown
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	// Keep application alive until interrupt
	logger.Log.Info("Press Ctrl+C to shutdown.")
	<-interrupt

	logger.Log.Info("Received interrupt, shutting down...")
	xpc.Stop()
}
