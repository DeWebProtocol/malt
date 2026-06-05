package framework

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = "malt-eval/v1"

// Plan is the top-level evaluation run plan consumed by `malt-eval`.
type Plan struct {
	RunID             string      `json:"run_id"`
	APIBaseURL        string      `json:"api_base_url,omitempty"`
	CASEndpoint       string      `json:"cas_endpoint,omitempty"`
	OutputDir         string      `json:"output_dir,omitempty"`
	ResultDir         string      `json:"result_dir,omitempty"`
	Suites            []SuitePlan `json:"suites"`
	outputDirExplicit bool
	resultDirExplicit bool
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
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Plan{}, fmt.Errorf("parse plan: unexpected trailing JSON")
		}
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	plan.outputDirExplicit = jsonHasKey(data, "output_dir")
	plan.resultDirExplicit = jsonHasKey(data, "result_dir")
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
		if err := validateRunID(p.RunID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(p.OutputDir) == "" {
		p.OutputDir = filepath.Join("output", p.RunID)
	} else {
		p.OutputDir = filepath.Clean(p.OutputDir)
	}
	if strings.TrimSpace(p.ResultDir) == "" {
		p.ResultDir = filepath.Join("result", p.RunID)
	} else {
		p.ResultDir = filepath.Clean(p.ResultDir)
	}
	if pathsOverlap(p.OutputDir, p.ResultDir) {
		return fmt.Errorf("output_dir and result_dir must not overlap")
	}
	var err error
	if p.APIBaseURL, err = normalizeHTTPURL("api_base_url", p.APIBaseURL); err != nil {
		return err
	}
	if p.CASEndpoint, err = normalizeHTTPURL("cas_endpoint", p.CASEndpoint); err != nil {
		return err
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

func normalizeHTTPURL(field, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%s is invalid: %w", field, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s must use http or https URL scheme", field)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%s must include a host", field)
	}
	return parsed.String(), nil
}

func validateRunID(runID string) error {
	if runID == "." || runID == ".." {
		return fmt.Errorf("run_id %q must not be a dot path segment", runID)
	}
	if strings.ContainsAny(runID, `/\`) {
		return fmt.Errorf("run_id %q must not contain path separators", runID)
	}
	return nil
}

// OverrideRunID applies a CLI run-id override. If the plan did not explicitly
// configure output_dir or result_dir, those directories are recomputed from the
// final run id during the next Normalize call.
func (p *Plan) OverrideRunID(runID string) {
	if p == nil {
		return
	}
	if strings.TrimSpace(runID) == "" {
		return
	}
	p.RunID = strings.TrimSpace(runID)
	if !p.outputDirExplicit {
		p.OutputDir = ""
	}
	if !p.resultDirExplicit {
		p.ResultDir = ""
	}
}

// OverrideOutputDir applies a CLI output-dir override and marks the directory
// as explicit so later run-id overrides do not rewrite it.
func (p *Plan) OverrideOutputDir(outputDir string) {
	if p == nil {
		return
	}
	if strings.TrimSpace(outputDir) == "" {
		return
	}
	p.OutputDir = strings.TrimSpace(outputDir)
	p.outputDirExplicit = true
}

// OverrideResultDir applies a CLI result-dir override and marks the directory
// as explicit so later run-id overrides do not rewrite it.
func (p *Plan) OverrideResultDir(resultDir string) {
	if p == nil {
		return
	}
	if strings.TrimSpace(resultDir) == "" {
		return
	}
	p.ResultDir = strings.TrimSpace(resultDir)
	p.resultDirExplicit = true
}

// OutputDirExplicit reports whether output_dir was set by the loaded plan or a CLI override.
func (p Plan) OutputDirExplicit() bool {
	return p.outputDirExplicit
}

// ResultDirExplicit reports whether result_dir was set by the loaded plan or a CLI override.
func (p Plan) ResultDirExplicit() bool {
	return p.resultDirExplicit
}

func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	cleanA := cleanAbsPath(a)
	cleanB := cleanAbsPath(b)
	return pathContains(cleanA, cleanB) || pathContains(cleanB, cleanA)
}

func cleanAbsPath(path string) string {
	clean := filepath.Clean(path)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return clean
	}
	return filepath.Clean(abs)
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return parent == child
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func jsonHasKey(data []byte, key string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return false
	}
	_, ok := object[key]
	return ok
}
