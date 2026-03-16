package atc

import (
	"os"
)

func init() {
	// This runs before any tests in this package
	// TODO: how can we make this work for custom config locations?
	// We move up two levels to the root of the repo so that config.yaml and /resources is found
	_ = os.Chdir("../../")
}
