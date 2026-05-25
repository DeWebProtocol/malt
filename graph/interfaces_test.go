package graph

import (
	"testing"

	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/writer"
)

func TestRuntimeGraphImplementsGraphContracts(t *testing.T) {
	var _ Resolver = (*resolver.Resolver)(nil)
	var _ Writer = (*writer.Writer)(nil)
}
