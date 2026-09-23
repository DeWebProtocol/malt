package rq2e0

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/engine"
	cid "github.com/ipfs/go-cid"
)

// BuildFixture computes exact typed KZG and IPA roots from declared source
// bytes, then independently checks the full source oracle for each graph.
func BuildFixture(ctx context.Context, source *rq2fixture.SourceDefinition) (*rq2fixture.Fixture, error) {
	if ctx == nil || source == nil {
		return nil, fmt.Errorf("RQ2 E0 fixture source or context is nil")
	}
	roots := make([]rq2fixture.RootBinding, 0, 2)
	for _, backend := range []string{"kzg", "ipa"} {
		e, err := fixtureEngine(backend)
		if err != nil {
			return nil, err
		}
		candidates, err := source.Candidates(ctx, e, backend)
		if err != nil {
			return nil, err
		}
		root, err := cid.Parse(candidates[len(candidates)-1].Root)
		if err != nil {
			return nil, err
		}
		roots = append(roots, rq2fixture.RootBinding{Backend: backend, CID: root.String()})
	}
	fixture, err := source.Fixture(roots)
	if err != nil {
		return nil, err
	}
	for _, backend := range []string{"kzg", "ipa"} {
		e, err := fixtureEngine(backend)
		if err != nil {
			return nil, err
		}
		if _, err := fixture.Candidates(ctx, e, backend); err != nil {
			return nil, err
		}
	}
	return fixture, nil
}
func fixtureEngine(backend string) (*engine.Engine, error) {
	var scheme engine.Profile
	var err error
	switch backend {
	case "kzg":
		scheme, err = kzg.NewScheme()
	case "ipa":
		scheme, err = ipa.NewScheme()
	default:
		return nil, fmt.Errorf("unsupported backend %q", backend)
	}
	if err != nil {
		return nil, err
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		return nil, err
	}
	return engine.New(profiles), nil
}
