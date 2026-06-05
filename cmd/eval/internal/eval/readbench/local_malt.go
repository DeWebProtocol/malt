package readbench

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/runtime/node"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	cid "github.com/ipfs/go-cid"
)

// LocalMALTSystem runs MALT resolve locally (no daemon) with a configurable
// mock CAS. This enables precise latency measurements without HTTP overhead.
type LocalMALTSystem struct {
	store *casmock.CAS
	node  *node.Node
	g     graph.Runtime
	root  cid.Cid
}

// NewLocalMALTSystem creates a MALT node backed by the given mock CAS,
// imports fixture files, and returns a ready-to-measure system.
func NewLocalMALTSystem(ctx context.Context, store *casmock.CAS, multiFix MultiDepthFixture) (*LocalMALTSystem, error) {
	cfg := config.DefaultConfig()
	cfg.State.KVStore.Type = "memory"

	n, err := node.NewNode(
		node.WithConfig(cfg),
		node.WithCAS(store),
	)
	if err != nil {
		return nil, fmt.Errorf("create local malt node: %w", err)
	}

	g, err := n.NewGraph("eval")
	if err != nil {
		return nil, fmt.Errorf("create local graph: %w", err)
	}

	// Build arc set from all fixture files at all depths.
	arcs := map[string]cid.Cid{}
	for _, fix := range multiFix.Fixtures {
		// Store file data in the mock CAS and get CIDs.
		smallCID, err := store.Put(ctx, fix.SmallData)
		if err != nil {
			return nil, fmt.Errorf("put small file at depth %d: %w", fix.Depth, err)
		}
		largeCID, err := store.Put(ctx, fix.LargeData)
		if err != nil {
			return nil, fmt.Errorf("put large file at depth %d: %w", fix.Depth, err)
		}
		arcs[fix.SmallPath] = smallCID
		arcs[fix.LargePath] = largeCID
	}

	// Every MALT-native map object requires a @payload binding.
	payloadCID, err := store.Put(ctx, []byte("eval-payload"))
	if err != nil {
		return nil, fmt.Errorf("put payload: %w", err)
	}
	arcs["@payload"] = payloadCID

	// Create root structure.
	snapshot, err := arcset.NewArcSet(arcs)
	if err != nil {
		return nil, fmt.Errorf("build arc set: %w", err)
	}
	root, err := g.Writer().CreateStructure(ctx, g.Namespace(), snapshot)
	if err != nil {
		return nil, fmt.Errorf("create structure: %w", err)
	}

	return &LocalMALTSystem{
		store: store,
		node:  n,
		g:     g,
		root:  root,
	}, nil
}

// MeasureResolve measures a single resolve_path operation at the given path.
func (s *LocalMALTSystem) MeasureResolve(ctx context.Context, iteration int, fixtureName string, filePath string) (*Result, error) {
	start := time.Now()
	result, err := s.g.Resolver().Resolve(s.root, filePath)
	elapsed := positiveElapsedNS(start, time.Now())
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", filePath, err)
	}

	return &Result{
		System:             SystemMALTFlat,
		OperationKind:      OperationResolvePath,
		Iteration:          iteration,
		FixtureName:        fixtureName,
		Path:               filePath,
		ElapsedNS:          elapsed,
		ProofListStepCount: len(result.Transcript.Steps),
		EvidenceItemCount:  len(result.Transcript.Steps),
		Target:             result.Target.String(),
	}, nil
}
