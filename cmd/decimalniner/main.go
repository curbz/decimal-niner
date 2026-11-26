package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	d9 "github.com/curbz/decimal-niner/internal"
	"github.com/curbz/decimal-niner/internal/atc"
	"github.com/curbz/decimal-niner/internal/configui"
	"github.com/curbz/decimal-niner/internal/logger"
	"github.com/curbz/decimal-niner/internal/mockserver"
	"github.com/curbz/decimal-niner/internal/traffic/trafficengines/d9traffic"
	"github.com/curbz/decimal-niner/internal/traffic/trafficengines/trafficglobal"
	"github.com/curbz/decimal-niner/internal/traffic/trafficengines/vat"
	"github.com/curbz/decimal-niner/internal/xplaneapi/xpconnect"
	"github.com/curbz/decimal-niner/pkg/util"
)

type d9config struct {
	D9 struct {
		LoggingLevel  string `yaml:"logging_level"`
		TrafficEngine string `yaml:"traffic_engine"`
		Resources     string `yaml:"resources_path"`
	} `yaml:"d9"`
	UI struct {
		AutostartApplication *bool `yaml:"autostart_application"`
		OpenInBrowser        *bool `yaml:"open_in_browser"`
	} `yaml:"ui"`
}

func main() {

	configFlag := flag.String("config", "", "Path to the config file (optional)")

	// mock server to emulate X-Plane REST+WebSocket
	mock := flag.Bool("mock", false, "start mock X-Plane server locally")

	// loopback configuration UI
	ui := flag.Bool("ui", false, "enable loopback configuration UI")
	uiAddr := flag.String("ui-addr", "127.0.0.1:8097", "configuration UI listen address")
	noBrowser := flag.Bool("no-open-browser", false, "do not open the configuration UI in a browser")

	applicationChild := flag.Bool("application-child", false, "run as an application child process")

	flag.Parse()

	_ = ui

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

	// The dashboard is the default parent mode. Application services run in a
	// separately controlled child process unless -application-child is used.
	if !*applicationChild {
		// When the configuration UI is requested, run only the local editor.
		// This allows setup and repair of missing or incomplete configuration values.
		{
			uiConfig, _ := util.LoadConfig[d9config](cfgPath)

			lifecycle := configui.NewProcessLifecycle(cfgPath, *mock)
			uiServer := configui.New(cfgPath, "web")
			uiServer.SetLifecycle(lifecycle)
			started, err := uiServer.Start(*uiAddr)
			if err != nil {
				log.Fatalf("failed to start configuration UI: %v", err)
			}

			autostart := uiConfig != nil && uiConfig.UI.AutostartApplication != nil && *uiConfig.UI.AutostartApplication
			if *mock {
				autostart = true
			}
			lifecycle.SetAutostarted(autostart)

			if autostart {
				if err := lifecycle.Start(); err != nil {
					log.Printf("could not autostart application: %v", err)
				}
			}

			openBrowser := uiConfig == nil || uiConfig.UI.OpenInBrowser == nil || *uiConfig.UI.OpenInBrowser
			log.Printf("configuration UI listening at http://%s", *uiAddr)
			if openBrowser && !*noBrowser {
				if err := configui.OpenBrowser(*uiAddr); err != nil {
					log.Printf("could not open browser automatically: %v", err)
				}
			}

			interrupt := make(chan os.Signal, 1)
			signal.Notify(interrupt, os.Interrupt)
			<-interrupt
			_ = lifecycle.Stop()
			_ = started.Close()
			return
		}
	}

	cfg, err := util.LoadConfig[d9config](cfgPath)
	if err != nil {
		log.Fatalf("Error reading configuration file: %v\n", err)
	}
	d9.Resources = cfg.D9.Resources

	// Initialize the logger once at start
	logger.Init(cfg.D9.LoggingLevel)

	if logger.Log == nil {
		log.Fatal("error initialising logger")
	}

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
						logger.Log.Infof("error closing mock server: %v", err)
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

	var te atc.TrafficEngine
	var teErr error
	switch cfg.D9.TrafficEngine {
	case "trafficglobal":
		te, teErr = trafficglobal.New(cfgPath)
	case "d9traffic":
		te, teErr = d9traffic.New(cfgPath)
	case "vat":
		te, teErr = vat.New(cfgPath)
	default:
		logger.Log.Fatalf("unsupported traffic engine specified in decimal-niner configuration: %s", cfg.D9.TrafficEngine)
		return
	}
	if teErr != nil {
		logger.Log.Fatalf("error initialising traffic engine: %v", err)
		return
	}

	// Get flight schedules
	fScheds, airports := te.LoadFlightPlans(te.GetFlightPlanFilePath())

	// Create ATC service
	atcService, err := atc.New(cfgPath, fScheds, airports)
	if err != nil {
		logger.Log.Info("failed to create ATC service, exiting")
		return
	}

	// set the airport service provider
	atcService.AirportService = atcService

	te.SetATCService(atcService)

	atcService.Run()
	te.Start()

	// Connect to X-Plane
	xpc := xpconnect.New(cfgPath, atcService, te.RequiresAircraftData())
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
