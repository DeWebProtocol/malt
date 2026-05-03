// Package api provides the application-level API for MALT.
// MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
// structures on top of content-addressed storage.
package api

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/arctable/versioned"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/kvstore/badger"
	"github.com/dewebprotocol/malt/core/kvstore/fs"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/dewebprotocol/malt/core/metrics"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	cid "github.com/ipfs/go-cid"
)

func canonicalArcTableType(t string) string {
	switch t {
	case "simple":
		return "overwrite"
	default:
		return t
	}
}

// Node is the stateless MALT node that holds shared infrastructure.
// It is the entry point for the MALT system and a factory for per-graph instances.
type Node struct {
	cfg  *config.Config
	opts *options

	// Shared components (namespace by namespace)
	kv           kvstore.KVStore
	arctable     arctable.ArcTable
	cas          cas.Reader
	graphManager *graph.Manager
	lineageMgr   *lineage.Manager

	metricsArcTable *metrics.ArcTable
	proofStats      metrics.ProofStatsRecorder
}

// NewNode creates a new MALT node with the given options.
//
// Example usage:
//
//	// Simple: use defaults
//	node, _ := api.NewNode()
//
//	// From config file
//	node, _ := api.NewNode(api.WithConfigFile("malt.json"))
//
//	// Custom components
//	node, _ := api.NewNode(
//	    api.WithKVStore(badger.New(badger.WithPath("./data"))),
//	)
func NewNode(opts ...Option) (*Node, error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	node := &Node{opts: options}

	// Load config if explicitly provided or file specified.
	if options.config != nil {
		node.cfg = options.config
	} else if options.configFile != "" {
		cfg, err := config.LoadFromFile(options.configFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
		node.cfg = cfg
	} else {
		cfg, err := config.Load()
		if err != nil {
			return nil, fmt.Errorf("failed to load default config: %w", err)
		}
		node.cfg = cfg
	}

	// Initialize components (use provided or create from config)
	var err error

	// KVStore
	if options.kvStore != nil {
		node.kv = options.kvStore
	} else {
		node.kv, err = node.initKVStore()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize KVStore: %w", err)
		}
	}

	// ArcTable
	if options.arctable != nil {
		node.arctable = options.arctable
	} else {
		err = node.initArcTable()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize ArcTable: %w", err)
		}
	}
	node.installMetricsArcTable()

	// Graph Manager
	node.graphManager = graph.NewManager(graph.NewStore(node.kv))

	// CAS
	if options.cas != nil {
		node.cas = options.cas
	} else {
		node.cas, err = node.initCAS()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize CAS: %w", err)
		}
	}

	return node, nil
}

func (n *Node) installMetricsArcTable() {
	if n.arctable == nil {
		return
	}
	if wrapped, ok := n.arctable.(*metrics.ArcTable); ok {
		n.metricsArcTable = wrapped
		return
	}
	wrapped := metrics.NewArcTable(n.arctable)
	n.arctable = wrapped
	n.metricsArcTable = wrapped
}

// initKVStore creates a KVStore from config.
func (n *Node) initKVStore() (kvstore.KVStore, error) {
	switch n.cfg.State.KVStore.Type {
	case "memory":
		return kvmemory.New(), nil
	case "badger":
		return badger.New(
			badger.WithPath(n.cfg.KVStorePath()),
			badger.WithInMemory(false),
		)
	case "fs":
		return fs.New(n.cfg.KVStorePath())
	default:
		return nil, fmt.Errorf("unknown kvstore type: %s", n.cfg.State.KVStore.Type)
	}
}

// initCommitmentSchemeType creates a commitment scheme from its configured type.
func (n *Node) initCommitmentSchemeType(kind string) (commitment.IndexCommitment, error) {
	switch kind {
	case "kzg":
		return kzg.NewScheme()
	case "ipa":
		return ipa.NewScheme()
	default:
		return nil, fmt.Errorf("unknown commitment type: %s", kind)
	}
}

// initArcTable creates an ArcTable from config.
func (n *Node) initArcTable() error {
	switch n.cfg.State.ArcTable.Type {
	case "simple", "overwrite":
		e, err := overwrite.NewArcTable(overwrite.WithKVStore(n.kv))
		if err != nil {
			return err
		}
		n.arctable = e
		return nil
	case "versioned":
		e, err := versioned.NewArcTable(versioned.WithKVStore(n.kv))
		if err != nil {
			return err
		}
		n.arctable = e
		return nil
	default:
		return fmt.Errorf("unknown arctable type: %s", n.cfg.State.ArcTable.Type)
	}
}

// initCAS creates a read-side CAS client from config.
func (n *Node) initCAS() (cas.Reader, error) {
	switch n.cfg.CAS.Mode {
	case "external", "embedded-mock":
		timeout, _ := n.cfg.CASTimeout()
		return ipfs.NewClient(
			n.cfg.CASBaseURL(),
			ipfs.WithTimeout(timeout),
		), nil
	case "mock":
		// Keep this mode only for tests or direct in-process injection paths.
		return casmock.NewCAS(casmock.WithoutLatency()), nil
	default:
		return nil, fmt.Errorf("unknown cas mode: %s", n.cfg.CAS.Mode)
	}
}

// CreateManagedGraph creates graph metadata using the node's runtime profile.
// The ArcTable implementation is node-scoped, so managed graphs always persist the
// node's active ArcTable type. Backend may vary per graph because commitment schemes
// are instantiated per graph.
func (n *Node) CreateManagedGraph(ctx context.Context, id string, backend string) (*graph.GraphMeta, error) {
	if backend == "" {
		backend = n.cfg.Structure.DefaultBackend
	}
	if _, err := n.initCommitmentSchemeType(backend); err != nil {
		return nil, err
	}
	return n.graphManager.CreateGraph(ctx, id, backend, canonicalArcTableType(n.cfg.State.ArcTable.Type))
}

// OpenGraph opens a managed graph using the runtime profile stored in GraphMeta.
// The persisted backend selects the commitment scheme; the persisted ArcTable type
// must match the node's shared ArcTable implementation.
func (n *Node) OpenGraph(ctx context.Context, id string) (*graph.Graph, error) {
	meta, err := n.graphManager.GetGraph(ctx, id)
	if err != nil {
		return nil, err
	}

	graphArcTableType := canonicalArcTableType(meta.ArcTableType)
	nodeArcTableType := canonicalArcTableType(n.cfg.State.ArcTable.Type)
	if graphArcTableType != "" && graphArcTableType != nodeArcTableType {
		return nil, fmt.Errorf("graph %q requires arctable_type %q, node is %q", id, meta.ArcTableType, n.cfg.State.ArcTable.Type)
	}

	backend := meta.Backend
	if backend == "" {
		backend = n.cfg.Structure.DefaultBackend
	}

	scheme, err := n.initCommitmentSchemeType(backend)
	if err != nil {
		return nil, fmt.Errorf("failed to create commitment scheme for graph %q: %w", id, err)
	}

	graphOpts := []graph.Option{
		graph.WithCommitmentScheme(scheme),
	}
	if lm := n.LineageManager(); lm != nil {
		graphOpts = append(graphOpts, graph.WithLineageRecorder(&lineageRecorderAdapter{mgr: lm}))
	}

	return graph.NewGraph(id, n.arctable, n.cas, graphOpts...)
}

// NewGraph creates a new ad hoc per-graph instance with its own per-graph
// semantic wiring, resolver, and writer. It does not consult GraphManager metadata.
//
// Parameters:
//   - id: unique graph identifier
//   - opts: functional options (graph.WithCommitmentScheme, graph.WithNamespace, etc.)
//
// The Node auto-injects shared infrastructure (ArcTable, CAS) and optional lineage recording.
func (n *Node) NewGraph(id string, opts ...graph.Option) (*graph.Graph, error) {
	o := &graph.Options{}
	for _, opt := range opts {
		opt(o)
	}

	// Default commitment scheme from config if not specified
	scheme := o.Scheme
	if scheme == nil {
		var err error
		scheme, err = n.initCommitmentSchemeType(n.cfg.Structure.DefaultBackend)
		if err != nil {
			return nil, fmt.Errorf("failed to create commitment scheme: %w", err)
		}
	}

	// Build graph options
	graphOpts := []graph.Option{
		graph.WithCommitmentScheme(scheme),
	}

	// Auto-inject lineage recorder if manager is available
	if lm := n.LineageManager(); lm != nil {
		graphOpts = append(graphOpts, graph.WithLineageRecorder(&lineageRecorderAdapter{mgr: lm}))
	}

	// Apply user options (they can override)
	graphOpts = append(graphOpts, opts...)

	return graph.NewGraph(id, n.arctable, n.cas, graphOpts...)
}

// lineageRecorderAdapter adapts lineage.Manager to writer.LineageRecorder.
type lineageRecorderAdapter struct {
	mgr *lineage.Manager
}

func (a *lineageRecorderAdapter) Record(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid) error {
	// namespace is ignored — lineage tracks by root CID
	return a.mgr.Record(ctx, newRoot, oldRoot, 0)
}

// Commitment returns the default commitment scheme type from config.
func (n *Node) Commitment() commitment.IndexCommitment {
	scheme, err := n.initCommitmentSchemeType(n.cfg.Structure.DefaultBackend)
	if err != nil {
		return nil
	}
	return scheme
}

// ArcTable returns the shared ArcTable.
func (n *Node) ArcTable() arctable.ArcTable {
	return n.arctable
}

// CAS returns the read-side CAS client.
func (n *Node) CAS() cas.Reader {
	return n.cas
}

// GraphManager returns the graph lifecycle manager.
func (n *Node) GraphManager() *graph.Manager {
	return n.graphManager
}

// LineageManager returns the lineage manager for version tracking.
// It is lazily initialized on first access.
func (n *Node) LineageManager() *lineage.Manager {
	if n.lineageMgr == nil {
		kv := lineage.NewKVStoreAdapter(n.kv)
		n.lineageMgr = lineage.NewManager(kv)
	}
	return n.lineageMgr
}

// KVStore returns the underlying KVStore.
func (n *Node) KVStore() kvstore.KVStore {
	return n.kv
}

// Config returns the node configuration.
func (n *Node) Config() *config.Config {
	return n.cfg
}

// MetricsSnapshot returns the current node-local evaluation counters.
func (n *Node) MetricsSnapshot() metrics.Snapshot {
	var snapshot metrics.Snapshot
	if n.cas != nil {
		if source, ok := n.cas.(interface{ SnapshotStats() metrics.CASStats }); ok {
			snapshot.CAS = source.SnapshotStats()
		}
	}
	if n.metricsArcTable != nil {
		snapshot.ArcTable = n.metricsArcTable.SnapshotStats()
	} else if n.arctable != nil {
		if source, ok := n.arctable.(interface{ SnapshotStats() metrics.ArcTableStats }); ok {
			snapshot.ArcTable = source.SnapshotStats()
		}
	}
	snapshot.Proof = n.proofStats.Snapshot()
	return snapshot
}

// ResetMetrics clears node-local evaluation counters where supported.
func (n *Node) ResetMetrics() {
	if n.cas != nil {
		if resetter, ok := n.cas.(interface{ ResetStats() }); ok {
			resetter.ResetStats()
		}
	}
	if n.metricsArcTable != nil {
		n.metricsArcTable.ResetStats()
	} else if n.arctable != nil {
		if resetter, ok := n.arctable.(interface{ ResetStats() }); ok {
			resetter.ResetStats()
		}
	}
	n.proofStats.Reset()
}

// RecordProofList records byte accounting for a verifier-facing proof artifact.
func (n *Node) RecordProofList(pl prooflist.ProofList) {
	n.proofStats.RecordProofList(pl)
}

// Close releases all resources.
func (n *Node) Close() error {
	var errs []error

	if n.arctable != nil {
		if err := n.arctable.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if n.kv != nil {
		if err := n.kv.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during close: %v", errs)
	}
	return nil
}
