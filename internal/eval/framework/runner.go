package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunOptions controls framework execution behavior.
type RunOptions struct {
	Clock func() time.Time
}

// Run executes all enabled suites in plan order.
func Run(ctx context.Context, plan Plan, registry Registry, opts RunOptions) error {
	if err := plan.Normalize(); err != nil {
		return err
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	for _, dir := range []string{"raw", "summary", "logs"} {
		if err := os.MkdirAll(filepath.Join(plan.OutputDir, dir), 0o755); err != nil {
			return err
		}
	}

	startedAt := clock().UTC().Format(time.RFC3339Nano)
	env := Env{
		RunID:     plan.RunID,
		OutputDir: plan.OutputDir,
		clock:     clock,
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		RunID:         plan.RunID,
		StartedAt:     startedAt,
		Machine:       collectMachineMetadata(),
	}

	for _, suitePlan := range plan.Suites {
		if !suitePlan.EnabledOrDefault() {
			continue
		}
		suite, ok := registry.Lookup(suitePlan.Name)
		if !ok {
			return fmt.Errorf("suite %q is not registered; available suites: %v", suitePlan.Name, registry.Names())
		}
		if err := suite.Run(ctx, env, suitePlan.Config); err != nil {
			return fmt.Errorf("run suite %s: %w", suitePlan.Name, err)
		}
		manifest.Suites = append(manifest.Suites, SuiteManifest{Name: suitePlan.Name})
	}
	manifest.FinishedAt = clock().UTC().Format(time.RFC3339Nano)
	return writeManifest(plan.OutputDir, manifest)
}

func writeManifest(outputDir string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(outputDir, "manifest.json"), data, 0o644)
}
