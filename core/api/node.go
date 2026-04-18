// Package api provides the application-level API for MALT.
// MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
// structures on top of content-addressed storage.
package api

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	"github.com/dewebprotocol/malt/core/eat/versioned"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/kvstore"
	"github.com/dewebprotocol/malt/core/kvstore/badger"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	cid "github.com/ipfs/go-cid"
)

func canonicalEATType(t string) string {
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

	// Shared components (namespace by bucketId)
	kv           kvstore.KVStore
	eat          eat.EAT
	cas          cas.Client
	graphManager *graph.Manager
	lineageMgr   *lineage.Manager
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
		// Use in-process defaults consistent with config.Init().
		node.cfg = &config.Config{
			CommitmentType: "kzg",
			KVStoreType:    "memory",
			EATType:        "versioned",
			CASType:        "mock",
		}
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

	// EAT
	if options.eat != nil {
		node.eat = options.eat
	} else {
		err = node.initEAT()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize EAT: %w", err)
		}
	}

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

// initKVStore creates a KVStore from config.
func (n *Node) initKVStore() (kvstore.KVStore, error) {
	switch n.cfg.KVStoreType {
	case "memory":
		return kvmemory.New(), nil
	case "badger":
		return badger.New(
			badger.WithPath(n.cfg.KVStore.Path),
			badger.WithInMemory(n.cfg.KVStore.InMemory),
		)
	default:
		return nil, fmt.Errorf("unknown kvstore type: %s", n.cfg.KVStoreType)
	}
}

// initCommitmentSchemeType creates a commitment scheme from its configured type.
func (n *Node) initCommitmentSchemeType(kind string) (commitment.Scheme, error) {
	switch kind {
	case "kzg":
		return kzg.NewScheme()
	default:
		return nil, fmt.Errorf("unknown commitment type: %s", kind)
	}
}

// initEAT creates an EAT from config.
func (n *Node) initEAT() error {
	switch n.cfg.EATType {
	case "simple", "overwrite":
		e, err := overwrite.NewEAT(overwrite.WithKVStore(n.kv))
		if err != nil {
			return err
		}
		n.eat = e
		return nil
	case "versioned":
		e, err := versioned.NewEAT(versioned.WithKVStore(n.kv))
		if err != nil {
			return err
		}
		n.eat = e
		return nil
	default:
		return fmt.Errorf("unknown eat type: %s", n.cfg.EATType)
	}
}

// initCAS creates a CAS client from config.
func (n *Node) initCAS() (cas.Client, error) {
	switch n.cfg.CASType {
	case "mock":
		var opts []casmock.Option
		if n.cfg.CAS.MockLatency != "" {
			d, err := n.cfg.CASMockLatency()
			if err != nil {
				return nil, fmt.Errorf("invalid mock_latency: %w", err)
			}
			opts = append(opts,
				casmock.WithGetLatency(d),
				casmock.WithPutLatency(d),
				casmock.WithHasLatency(d),
			)
		}
		return casmock.NewCAS(opts...), nil
	case "ipfs-gateway":
		timeout, _ := n.cfg.CASTimeout()
		return ipfs.NewClient(
			n.cfg.CAS.GatewayURL,
			ipfs.WithTimeout(timeout),
		), nil
	default:
		return nil, fmt.Errorf("unknown cas type: %s", n.cfg.CASType)
	}
}

// CreateManagedGraph creates graph metadata using the node's runtime profile.
// The EAT implementation is node-scoped, so managed graphs always persist the
// node's active EAT type. Backend may vary per graph because commitment schemes
// are instantiated per graph.
func (n *Node) CreateManagedGraph(ctx context.Context, id string, backend string) (*graph.GraphMeta, error) {
	if backend == "" {
		backend = n.cfg.CommitmentType
	}
	if _, err := n.initCommitmentSchemeType(backend); err != nil {
		return nil, err
	}
	return n.graphManager.CreateGraph(ctx, id, backend, canonicalEATType(n.cfg.EATType))
}

// OpenGraph opens a managed graph using the runtime profile stored in GraphMeta.
// The persisted backend selects the commitment scheme; the persisted EAT type
// must match the node's shared EAT implementation.
func (n *Node) OpenGraph(ctx context.Context, id string) (*graph.Graph, error) {
	meta, err := n.graphManager.GetGraph(ctx, id)
	if err != nil {
		return nil, err
	}

	graphEATType := canonicalEATType(meta.EATType)
	nodeEATType := canonicalEATType(n.cfg.EATType)
	if graphEATType != "" && graphEATType != nodeEATType {
		return nil, fmt.Errorf("graph %q requires eat_type %q, node is %q", id, meta.EATType, n.cfg.EATType)
	}

	backend := meta.Backend
	if backend == "" {
		backend = n.cfg.CommitmentType
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

	return graph.NewGraph(id, n.eat, n.cas, graphOpts...)
}

// NewGraph creates a new ad hoc per-graph instance with its own SCE, resolver,
// and writer. It does not consult GraphManager metadata.
//
// Parameters:
//   - id: unique graph identifier
//   - opts: functional options (graph.WithCommitmentScheme, graph.WithBucketId, etc.)
//
// The Node auto-injects shared infrastructure (EAT, CAS) and optional lineage recording.
func (n *Node) NewGraph(id string, opts ...graph.Option) (*graph.Graph, error) {
	o := &graph.Options{}
	for _, opt := range opts {
		opt(o)
	}

	// Default commitment scheme from config if not specified
	scheme := o.Scheme
	if scheme == nil {
		var err error
		scheme, err = n.initCommitmentSchemeType(n.cfg.CommitmentType)
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

	return graph.NewGraph(id, n.eat, n.cas, graphOpts...)
}

// lineageRecorderAdapter adapts lineage.Manager to writer.LineageRecorder.
type lineageRecorderAdapter struct {
	mgr *lineage.Manager
}

func (a *lineageRecorderAdapter) Record(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid) error {
	// bucketId is ignored — lineage tracks by root CID
	return a.mgr.Record(ctx, newRoot, oldRoot, 0)
}

// Commitment returns the default commitment scheme type from config.
func (n *Node) Commitment() commitment.Scheme {
	scheme, err := n.initCommitmentSchemeType(n.cfg.CommitmentType)
	if err != nil {
		return nil
	}
	return scheme
}

// EAT returns the shared EAT.
func (n *Node) EAT() eat.EAT {
	return n.eat
}

// CAS returns the CAS client.
func (n *Node) CAS() cas.Client {
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

// Close releases all resources.
func (n *Node) Close() error {
	var errs []error

	if n.eat != nil {
		if err := n.eat.Close(); err != nil {
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
