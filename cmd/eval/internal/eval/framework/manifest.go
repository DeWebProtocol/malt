package framework

import (
	"os"
	"runtime"
)

// Manifest records enough metadata to make an evaluation run auditable.
type Manifest struct {
	SchemaVersion string          `json:"schema_version"`
	RunID         string          `json:"run_id"`
	StartedAt     string          `json:"started_at"`
	FinishedAt    string          `json:"finished_at"`
	Machine       MachineMetadata `json:"machine"`
	Suites        []SuiteManifest `json:"suites"`
}

// MachineMetadata captures local runtime metadata.
type MachineMetadata struct {
	Hostname  string `json:"hostname,omitempty"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
	GoVersion string `json:"go_version"`
}

// SuiteManifest records a suite that actually ran.
type SuiteManifest struct {
	Name string `json:"name"`
}

func collectMachineMetadata() MachineMetadata {
	host, _ := os.Hostname()
	return MachineMetadata{
		Hostname:  host,
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
		GoVersion: runtime.Version(),
	}
}
