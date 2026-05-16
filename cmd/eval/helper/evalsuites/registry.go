// Package evalsuites assembles the suite registry used by malt-eval run.
package evalsuites

import "github.com/dewebprotocol/malt/internal/eval/framework"

// NewRegistry returns the production suite registry.
func NewRegistry() framework.Registry {
	return framework.NewRegistry()
}
