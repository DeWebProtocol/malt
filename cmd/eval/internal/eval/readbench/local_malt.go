package readbench

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/config"
	runtimegraph "github.com/dewebprotocol/malt/runtime/graph"
	"github.com/dewebprotocol/malt/runtime/node"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	cid "github.com/ipfs/go-cid"
)

// LocalMALTSystem runs MALT resolve locally (no daemon) with a configurable
// mock CAS. This enables precise latency measurements without HTTP overhead.
type LocalMALTSystem struct {
	store *casmock.CAS
	node  *node.Node
	g     *runtimegraph.RuntimeGraph
	root  cid.Cid
}

// NewLocalMALTSystem creates a MALT node backed by the given mock CAS,
// imports fixture files, and returns a ready-to-measure system.
func NewLocalMALTSystem(ctx context.Context, store *casmock.CAS, multiFix MultiDepthFixture) (*LocalMALTSystem, error) {
	files := make([]MatrixDatasetFile, 0, len(multiFix.Fixtures)*2)
	for _, fix := range multiFix.Fixtures {
		files = append(files, MatrixDatasetFile{
			Path:  fix.SmallPath,
			Data:  fix.SmallData,
			Depth: fix.Depth,
			Role:  "small",
		})
		if fix.LargePath != "" {
			files = append(files, MatrixDatasetFile{
				Path:  fix.LargePath,
				Data:  fix.LargeData,
				Depth: fix.Depth,
				Role:  "large",
			})
		}
	}
	return NewLocalMALTSystemWithFiles(ctx, store, files)
}

// NewLocalMALTSystemWithFiles creates a flat authenticated MALT structure from
// explicit logical files.
func NewLocalMALTSystemWithFiles(ctx context.Context, store *casmock.CAS, files []MatrixDatasetFile) (*LocalMALTSystem, error) {
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
	for _, file := range files {
		if file.Path == "" {
			return nil, fmt.Errorf("empty file path in local malt system")
		}
		payloadCID, err := store.Put(ctx, file.Data)
		if err != nil {
			return nil, fmt.Errorf("put file %q: %w", file.Path, err)
		}
		arcs[file.Path] = payloadCID
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
	s.node.ResetMetrics()
	start := time.Now()
	result, err := s.g.Resolver().Resolve(ctx, s.root, filePath)
	elapsed := positiveElapsedNS(start, time.Now())
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", filePath, err)
	}
	snapshot := s.node.MetricsSnapshot()

	return &Result{
		System:             SystemMALTFlat,
		OperationKind:      OperationResolvePath,
		Workload:           WorkloadDeepPathLookup,
		Iteration:          iteration,
		FixtureName:        fixtureName,
		Path:               filePath,
		ElapsedNS:          elapsed,
		ProofListStepCount: len(result.Transcript.Steps),
		EvidenceItemCount:  len(result.Transcript.Steps),
		Target:             result.Target.String(),
		CAS:                snapshot.CAS,
		ArcTable:           snapshot.ArcTable,
		Proof:              snapshot.Proof,
	}, nil
}
