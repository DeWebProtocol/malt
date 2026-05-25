package graph

import (
	"testing"

	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/writer"
)

func TestRuntimeGraphImplementsGraphContracts(t *testing.T) {
	var _ Graph = (*RuntimeGraph)(nil)
	var _ Resolver = (*resolver.Resolver)(nil)
	var _ Writer = (*writer.Writer)(nil)
}
