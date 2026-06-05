package framework

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Suite is one independently runnable evaluation workload.
type Suite interface {
	Name() string
	Run(context.Context, Env, json.RawMessage) error
}

// DaemonRequiringSuite is implemented by suites that require a live MALT daemon.
type DaemonRequiringSuite interface {
	RequiresDaemon() bool
}

// ConfigDaemonRequiringSuite is implemented by suites whose daemon dependency
// depends on the suite-specific plan configuration.
type ConfigDaemonRequiringSuite interface {
	RequiresDaemonForConfig(json.RawMessage) (bool, error)
}

// Registry maps suite names to implementations.
type Registry struct {
	suites map[string]Suite
}

// NewRegistry creates an empty suite registry.
func NewRegistry() Registry {
	return Registry{suites: make(map[string]Suite)}
}

// Register adds a suite implementation.
func (r Registry) Register(suite Suite) error {
	if suite == nil {
		return fmt.Errorf("suite is nil")
	}
	name := suite.Name()
	if name == "" {
		return fmt.Errorf("suite name is empty")
	}
	if _, exists := r.suites[name]; exists {
		return fmt.Errorf("suite %q already registered", name)
	}
	r.suites[name] = suite
	return nil
}

// Lookup returns a registered suite by name.
func (r Registry) Lookup(name string) (Suite, bool) {
	suite, ok := r.suites[name]
	return suite, ok
}

// Names returns registered suite names in stable order.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r.suites))
	for name := range r.suites {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
