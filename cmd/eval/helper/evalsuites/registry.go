// Package evalsuites assembles the suite registry used by malt-eval run.
package evalsuites

import (
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/casmodel"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/flatindexcardinality"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/proofoverhead"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/readdepthsweep"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/readlatencysweep"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/readmatrix"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/readquery"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/storageoverhead"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/writetrace"
)

// NewRegistry returns the production suite registry.
func NewRegistry() framework.Registry {
	registry := framework.NewRegistry()
	mustRegister(registry, writetrace.Suite{})
	mustRegister(registry, readmatrix.Suite{})
	mustRegister(registry, flatindexcardinality.Suite{})
	mustRegister(registry, readquery.Suite{})
	mustRegister(registry, readdepthsweep.Suite{})
	mustRegister(registry, readlatencysweep.Suite{})
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
