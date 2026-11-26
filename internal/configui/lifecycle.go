package configui

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	"gopkg.in/yaml.v3"
)

// LifecycleStatus describes the application process state.
type LifecycleStatus struct {
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	ProcessID     int    `json:"pid,omitempty"`
	Autostarted   bool   `json:"autostarted"`
	TrafficEngine string `json:"traffic_engine,omitempty"`
}

// Lifecycle controls starting and stopping the application process.
type Lifecycle interface {
	Status() LifecycleStatus
	Start() error
	Stop() error
}

// Lifecycle controls the application process independently of the dashboard.
type processLifecycle struct {
	autostarted bool
	stopping    bool
	lastError   string
	state       string
	cmd         *exec.Cmd
	mock        bool
	configPath  string
	mu          sync.Mutex
}

func NewProcessLifecycle(configPath string, mock bool) *processLifecycle {
	return &processLifecycle{configPath: configPath, mock: mock, state: "stopped"}
}

func (l *processLifecycle) Start() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cmd != nil && l.cmd.Process != nil && l.state == "running" {
		return fmt.Errorf("application is already running")
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}

	args := []string{"-application-child", "-config", l.configPath}
	if l.mock {
		args = append(args, "-mock")
	}

	cmd := exec.Command(executable, args...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	l.cmd = cmd
	l.state = "starting"
	l.lastError = ""
	l.stopping = false

	go func() {
		err := cmd.Wait()
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.stopping {
			l.lastError = ""
			l.state = "stopped"
			l.stopping = false
		} else if err != nil {
			l.lastError = err.Error()
			l.state = "error"
			l.stopping = false
		} else {
			l.state = "stopped"
		}
		l.cmd = nil
	}()

	l.state = "running"
	return nil
}

func (l *processLifecycle) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cmd == nil || l.cmd.Process == nil {
		return nil
	}

	l.state = "stopping"
	l.stopping = true
	if err := l.cmd.Process.Kill(); err != nil {
		l.lastError = err.Error()
		l.state = "error"
		return err
	}

	l.cmd = nil
	l.lastError = ""
	l.state = "stopped"
	return nil
}

func (l *processLifecycle) Status() LifecycleStatus {
	l.mu.Lock()
	defer l.mu.Unlock()

	status := LifecycleStatus{State: l.state, Error: l.lastError, Autostarted: l.autostarted}
	if l.cmd != nil && l.cmd.Process != nil {
		status.ProcessID = l.cmd.Process.Pid
	}

	if data, err := os.ReadFile(l.configPath); err == nil {
		var cfg struct {
			D9 struct {
				TrafficEngine string `yaml:"traffic_engine"`
			} `yaml:"d9"`
		}
		if err := yaml.Unmarshal(data, &cfg); err == nil {
			status.TrafficEngine = cfg.D9.TrafficEngine
		}
	}

	return status
}

func (l *processLifecycle) SetAutostarted(value bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.autostarted = value
}
