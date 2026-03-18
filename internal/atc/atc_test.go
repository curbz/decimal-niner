package atc

import (
	"log"
	"os"
)

	// This runs before any tests in this package
func init() {

	log.Println("running prerequisite atc package init function")

	// Check for custom config file location
	configPath := os.Getenv("D9_CONFIG_PATH")

	if configPath == "" {
		// Use default config location.
		// Move up two levels to the root of the repo so that config.yaml and /resources is found
		configPath = "../.."
	} else {
		log.Println("loading configuration from custom location", configPath)
	}

	err := os.Chdir(configPath)
	if err != nil {
		log.Fatalf("test execution failed - error loading configuration: %v", err)
	}

}
