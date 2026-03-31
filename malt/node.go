// Package malt provides the application-level API for MALT.
// MALT (Mutable structure LAyer on Top) provides verifiable, evolvable
// structures on top of content-addressed storage.
package malt

import (
	"fmt"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/internal/cas"
	"github.com/dewebprotocol/malt/internal/cas/ipfsgateway"
	casmock "github.com/dewebprotocol/malt/internal/cas/mock"
	"github.com/dewebprotocol/malt/internal/eat"
	"github.com/dewebprotocol/malt/internal/eat/simple"
	"github.com/dewebprotocol/malt/internal/eat/versioned"
	"github.com/dewebprotocol/malt/internal/kv"
	"github.com/dewebprotocol/malt/internal/kv/badger"
	kvmemory "github.com/dewebprotocol/malt/internal/kv/memory"
	"github.com/dewebprotocol/malt/internal/resolver"
	"github.com/dewebprotocol/malt/internal/sce"
	"github.com/dewebprotocol/malt/internal/sce/ipa"
	"github.com/dewebprotocol/malt/internal/sce/kzg"
	"github.com/dewebprotocol/malt/internal/sce/verkle"
)

// Node is the main MALT runtime that holds all components.
// It is the entry point for the MALT system.
type Node struct {
	config *config.Config

	// Core components (injected based on config)
	kv         kv.KVStore
	commitment sce.CommitmentScheme
	eat        eat.EAT
	cas        cas.Client
	resolver   *resolver.Resolver
}

// NewNode creates a new MALT node with the given configuration.
// It initializes all components based on the configuration.
func NewNode(cfg *config.Config) (*Node, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	node := &Node{config: cfg}

	// Initialize KVStore
	var err error
	node.kv, err = node.initKVStore()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize KVStore: %w", err)
	}

	// Initialize Commitment Scheme
	node.commitment, err = node.initCommitment()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize commitment scheme: %w", err)
	}

	// Initialize EAT
	node.eat, err = node.initEAT()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize EAT: %w", err)
	}

	// Initialize CAS
	node.cas, err = node.initCAS()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize CAS: %w", err)
	}

	// Initialize Resolver
	node.resolver = resolver.NewResolver(node.eat, node.commitment, node.cas)

	return node, nil
}

// initKVStore initializes the KVStore based on configuration.
func (n *Node) initKVStore() (kv.KVStore, error) {
	switch n.config.KVStoreType {
	case "memory":
		return kvmemory.New(), nil
	case "badger":
		return badger.New(&badger.Config{
			Path:     n.config.KVStore.Path,
			InMemory: n.config.KVStore.InMemory,
		})
	default:
		return nil, fmt.Errorf("unknown kvstore type: %s", n.config.KVStoreType)
	}
}

// initCommitment initializes the commitment scheme based on configuration.
func (n *Node) initCommitment() (sce.CommitmentScheme, error) {
	switch n.config.CommitmentType {
	case "kzg":
		return kzg.NewCommitment()
	case "verkle":
		return verkle.NewCommitment()
	case "ipa":
		return ipa.NewCommitment(nil) // uses default config
	default:
		return nil, fmt.Errorf("unknown commitment type: %s", n.config.CommitmentType)
	}
}

// initEAT initializes the EAT based on configuration.
func (n *Node) initEAT() (eat.EAT, error) {
	switch n.config.EATType {
	case "simple":
		return simple.NewEAT(), nil
	case "versioned":
		return versioned.NewEAT(n.kv)
	default:
		return nil, fmt.Errorf("unknown eat type: %s", n.config.EATType)
	}
}

// initCAS initializes the CAS client based on configuration.
func (n *Node) initCAS() (cas.Client, error) {
	switch n.config.CASType {
	case "mock":
		return casmock.NewCAS(), nil
	case "ipfs-gateway":
		timeout, err := n.config.CASTimeout()
		if err != nil {
			timeout = 30e9 // 30s default
		}
		return ipfsgateway.NewClient(&ipfsgateway.Config{
			GatewayURL: n.config.CAS.GatewayURL,
			Timeout:    timeout,
		}), nil
	default:
		return nil, fmt.Errorf("unknown cas type: %s", n.config.CASType)
	}
}

// Commitment returns the commitment scheme.
func (n *Node) Commitment() sce.CommitmentScheme {
	return n.commitment
}

// EAT returns the EAT.
func (n *Node) EAT() eat.EAT {
	return n.eat
}

// CAS returns the CAS client.
func (n *Node) CAS() cas.Client {
	return n.cas
}

// Resolver returns the resolver.
func (n *Node) Resolver() *resolver.Resolver {
	return n.resolver
}

// KVStore returns the underlying KVStore.
func (n *Node) KVStore() kv.KVStore {
	return n.kv
}

// Config returns the node configuration.
func (n *Node) Config() *config.Config {
	return n.config
}

// NewStructure creates a new structure from an arc set.
// This is a convenience method that uses the node's components.
func (n *Node) NewStructure(arcs sce.ArcSetView) (*Structure, error) {
	return NewStructure(arcs, n.eat, n.commitment)
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