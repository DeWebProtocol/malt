// Package malt provides the application-level API for MALT.
// MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
// structures on top of content-addressed storage.
package malt

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/cas/ipfsgateway"
	casmock "github.com/dewebprotocol/malt/cas/mock"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/eat/memory"
	"github.com/dewebprotocol/malt/core/eat/versioned"
	"github.com/dewebprotocol/malt/core/types/kvstore"
	"github.com/dewebprotocol/malt/core/types/kvstore/badger"
	kvmemory "github.com/dewebprotocol/malt/core/types/kvstore/memory"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/explicit"
	"github.com/dewebprotocol/malt/core/resolver/implicit"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/sce/commitment/verkle"
	"github.com/dewebprotocol/malt/gateway"
)

// Node is the main MALT runtime that holds all components.
// It is the entry point for the MALT system.
type Node struct {
	cfg *config.Config
	opts *options

	// Core components
	kv              kvstore.KVStore
	sce             *sce.Engine
	eat             eat.EAT
	cas             cas.Client
	explicitResolver resolver.Resolver
	implicitResolver resolver.Resolver
	gateway         *gateway.Gateway
}

// NewNode creates a new MALT node with the given options.
//
// Example usage:
//
//	// Simple: use defaults
//	node, _ := malt.NewNode()
//
//	// From config file
//	node, _ := malt.NewNode(malt.WithConfigFile("malt.json"))
//
//	// Custom components
//	node, _ := malt.NewNode(
//	    malt.WithKVStore(badger.New(badger.WithPath("./data"))),
//	    malt.WithCommitment(kzg.NewCommitment()),
//	)
func NewNode(opts ...Option) (*Node, error) {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	node := &Node{opts: options}

	// Load config if file specified
	if options.configFile != "" {
		cfg, err := config.LoadFromFile(options.configFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
		node.cfg = cfg
	} else {
		// Use empty config with defaults
		node.cfg = &config.Config{
			CommitmentType: "kzg",
			KVStoreType:    "memory",
			EATType:        "simple",
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

	// Commitment
	if options.commitment != nil {
		node.sce = sce.NewEngine(options.commitment)
	} else {
		scheme, err := node.initCommitmentScheme()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize commitment scheme: %w", err)
		}
		node.sce = sce.NewEngine(scheme)
	}

	// EAT
	if options.eat != nil {
		node.eat = options.eat
	} else {
		node.eat, err = node.initEAT()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize EAT: %w", err)
		}
	}

	// CAS
	if options.cas != nil {
		node.cas = options.cas
	} else {
		node.cas, err = node.initCAS()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize CAS: %w", err)
		}
	}

	// Explicit resolver (MALT arcs)
	node.explicitResolver = explicit.NewResolver(node.eat, node.sce)

	// Implicit resolver (Merkle DAG via CAS)
	node.implicitResolver = implicit.NewResolver(node.cas)

	// Gateway (full path resolution)
	node.gateway = gateway.NewGateway(node.explicitResolver, node.implicitResolver)

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

// initCommitmentScheme creates a commitment scheme from config.
func (n *Node) initCommitmentScheme() (commitment.Scheme, error) {
	switch n.cfg.CommitmentType {
	case "kzg":
		return kzg.NewScheme()
	case "verkle":
		return verkle.NewScheme()
	case "ipa":
		return ipa.NewScheme()
	default:
		return nil, fmt.Errorf("unknown commitment type: %s", n.cfg.CommitmentType)
	}
}

// initEAT creates an EAT from config.
func (n *Node) initEAT() (eat.EAT, error) {
	switch n.cfg.EATType {
	case "simple", "memory":
		return memory.NewBucketedInMemoryEAT(), nil
	case "versioned":
		return versioned.NewEAT(n.kv)
	default:
		return nil, fmt.Errorf("unknown eat type: %s", n.cfg.EATType)
	}
}

// initCAS creates a CAS client from config.
func (n *Node) initCAS() (cas.Client, error) {
	switch n.cfg.CASType {
	case "mock":
		return casmock.NewCAS(), nil
	case "ipfs-gateway":
		timeout, _ := n.cfg.CASTimeout()
		return ipfsgateway.NewClient(
			ipfsgateway.WithGatewayURL(n.cfg.CAS.GatewayURL),
			ipfsgateway.WithTimeout(timeout),
		), nil
	default:
		return nil, fmt.Errorf("unknown cas type: %s", n.cfg.CASType)
	}
}

// SCE returns the SCE engine.
func (n *Node) SCE() *sce.Engine {
	return n.sce
}

// Commitment returns the underlying commitment scheme.
func (n *Node) Commitment() commitment.Scheme {
	// Return the underlying scheme from the engine
	return n.sce.Scheme()
}

// EAT returns the EAT.
func (n *Node) EAT() eat.EAT {
	return n.eat
}

// CAS returns the CAS client.
func (n *Node) CAS() cas.Client {
	return n.cas
}

// Resolver returns the explicit resolver for MALT arcs.
func (n *Node) Resolver() resolver.Resolver {
	return n.explicitResolver
}

// Gateway returns the gateway for full path resolution.
func (n *Node) Gateway() *gateway.Gateway {
	return n.gateway
}

// KVStore returns the underlying KVStore.
func (n *Node) KVStore() kvstore.KVStore {
	return n.kv
}

// Config returns the node configuration.
func (n *Node) Config() *config.Config {
	return n.cfg
}

// NewStructure creates a new structure from an arc set.
func (n *Node) NewStructure(arcs arcset.View) (*Structure, error) {
	return NewStructure(arcs, n.eat, n.sce)
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