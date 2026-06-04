package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/summary"
)

// RunOptions controls framework execution behavior.
type RunOptions struct {
	Clock  func() time.Time
	Stderr *os.File // If nil, progress logs are discarded.
}

// Run executes all enabled suites in plan order.
func Run(ctx context.Context, plan Plan, registry Registry, opts RunOptions) error {
	if err := plan.Normalize(); err != nil {
		return err
	}
	if err := preflightSuites(plan, registry); err != nil {
		return err
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	if err := prepareResultLayout(plan.ResultDir); err != nil {
		return err
	}
	if err := prepareOutputLayout(plan.OutputDir); err != nil {
		return err
	}

	log := newLogger(opts.Stderr)

	startedAt := clock().UTC().Format(time.RFC3339Nano)
	env := Env{
		RunID:       plan.RunID,
		APIBaseURL:  plan.APIBaseURL,
		CASEndpoint: plan.CASEndpoint,
		OutputDir:   plan.OutputDir,
		ResultDir:   plan.ResultDir,
		clock:       clock,
		Logf:        log,
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		RunID:         plan.RunID,
		StartedAt:     startedAt,
		Machine:       collectMachineMetadata(),
	}

	total := countEnabled(plan.Suites)
	log("running %d suite(s)", total)
	for i, suitePlan := range plan.Suites {
		if !suitePlan.EnabledOrDefault() {
			continue
		}
		suite, ok := registry.Lookup(suitePlan.Name)
		if !ok {
			return fmt.Errorf("suite %q is not registered; available suites: %v", suitePlan.Name, registry.Names())
		}
		log("[%d/%d] suite %s started", i+1, total, suitePlan.Name)
		suiteStart := clock()
		if err := suite.Run(ctx, env, suitePlan.Config); err != nil {
			return fmt.Errorf("run suite %s: %w", suitePlan.Name, err)
		}
		log("[%d/%d] suite %s finished (%s)", i+1, total, suitePlan.Name, clock().Sub(suiteStart).Round(time.Millisecond))
		manifest.Suites = append(manifest.Suites, SuiteManifest{Name: suitePlan.Name})
	}
	if err := summary.Summarize(plan.ResultDir, filepath.Join(plan.ResultDir, "summary")); err != nil {
		return fmt.Errorf("summarize run: %w", err)
	}
	manifest.FinishedAt = clock().UTC().Format(time.RFC3339Nano)
	return writeManifest(plan.ResultDir, manifest)
}

func countEnabled(suites []SuitePlan) int {
	n := 0
	for _, s := range suites {
		if s.EnabledOrDefault() {
			n++
		}
	}
	return n
}

func newLogger(stderr *os.File) func(string, ...any) {
	if stderr == nil {
		stderr = os.Stderr
	}
	return func(format string, args ...any) {
		fmt.Fprintf(stderr, format+"\n", args...)
	}
}

func preflightSuites(plan Plan, registry Registry) error {
	for _, suitePlan := range plan.Suites {
		if !suitePlan.EnabledOrDefault() {
			continue
		}
		if _, ok := registry.Lookup(suitePlan.Name); !ok {
			return fmt.Errorf("suite %q is not registered; available suites: %v", suitePlan.Name, registry.Names())
		}
	}
	return nil
}

func prepareResultLayout(resultDir string) error {
	manifestPath := filepath.Join(resultDir, "manifest.json")
	if err := os.Remove(manifestPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, dir := range []string{"raw", "summary"} {
		path := filepath.Join(resultDir, dir)
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func prepareOutputLayout(outputDir string) error {
	if err := os.RemoveAll(outputDir); err != nil {
		return err
	}
	for _, dir := range []string{"", "logs"} {
		if err := os.MkdirAll(filepath.Join(outputDir, dir), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeManifest(resultDir string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(resultDir, "manifest.json"), data, 0o644)
}
