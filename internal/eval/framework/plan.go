package framework

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = "malt-eval/v1"

// Plan is the top-level evaluation run plan consumed by `malt-eval run`.
type Plan struct {
	RunID     string      `json:"run_id"`
	OutputDir string      `json:"output_dir,omitempty"`
	Suites    []SuitePlan `json:"suites"`
}

// SuitePlan configures one registered evaluation suite.
type SuitePlan struct {
	Name    string          `json:"name"`
	Enabled *bool           `json:"enabled,omitempty"`
	Config  json.RawMessage `json:"config,omitempty"`
}

// EnabledOrDefault returns true unless the plan explicitly disables the suite.
func (p SuitePlan) EnabledOrDefault() bool {
	return p.Enabled == nil || *p.Enabled
}

// LoadPlan reads and normalizes an evaluation run plan from disk.
func LoadPlan(path string) (Plan, error) {
	if strings.TrimSpace(path) == "" {
		return Plan{}, fmt.Errorf("plan path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	if err := plan.Normalize(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// Normalize fills defaults and validates suite names.
func (p *Plan) Normalize() error {
	if p == nil {
		return fmt.Errorf("plan is nil")
	}
	if strings.TrimSpace(p.RunID) == "" {
		p.RunID = "run-" + time.Now().UTC().Format("20060102T150405Z")
	} else {
		p.RunID = strings.TrimSpace(p.RunID)
	}
	if strings.TrimSpace(p.OutputDir) == "" {
		p.OutputDir = filepath.Join("results", p.RunID)
	} else {
		p.OutputDir = filepath.Clean(p.OutputDir)
	}
	if len(p.Suites) == 0 {
		return fmt.Errorf("at least one suite is required")
	}
	for i := range p.Suites {
		name := strings.TrimSpace(p.Suites[i].Name)
		if name == "" {
			return fmt.Errorf("suite %d name is empty", i)
		}
		p.Suites[i].Name = name
	}
	return nil
}
