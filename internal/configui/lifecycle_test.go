package configui

import (
	"testing"
)

func TestNewProcessLifecycleStartsStopped(t *testing.T) {
	lifecycle := NewProcessLifecycle("config.yaml", false)
	status := lifecycle.Status()
	if status.State != "stopped" {
		t.Fatalf("initial state = %q, want stopped", status.State)
	}
}

func TestProcessLifecycleStatusAutostart(t *testing.T) {
	lifecycle := NewProcessLifecycle("config.yaml", false)
	lifecycle.SetAutostarted(true)
	status := lifecycle.Status()
	if !status.Autostarted {
		t.Fatal("autostarted status was not recorded")
	}
}
