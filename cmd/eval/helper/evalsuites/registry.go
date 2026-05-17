// Package evalsuites assembles the suite registry used by malt-eval run.
package evalsuites

import (
	"github.com/dewebprotocol/malt/internal/eval/framework"
	"github.com/dewebprotocol/malt/internal/eval/suites/casmodel"
	"github.com/dewebprotocol/malt/internal/eval/suites/proofoverhead"
	"github.com/dewebprotocol/malt/internal/eval/suites/readquery"
	"github.com/dewebprotocol/malt/internal/eval/suites/storageoverhead"
	"github.com/dewebprotocol/malt/internal/eval/suites/writetrace"
)

// NewRegistry returns the production suite registry.
func NewRegistry() framework.Registry {
	registry := framework.NewRegistry()
	mustRegister(registry, writetrace.Suite{})
	mustRegister(registry, readquery.Suite{})
	mustRegister(registry, casmodel.Suite{})
	mustRegister(registry, proofoverhead.Suite{})
	mustRegister(registry, storageoverhead.Suite{})
	return registry
}

func mustRegister(registry framework.Registry, suite framework.Suite) {
	if err := registry.Register(suite); err != nil {
		panic(err)
	}
}
