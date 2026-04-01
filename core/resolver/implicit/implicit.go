// Package implicit implements the Resolver interface for Merkle-DAG traversal.
// It fetches blocks from CAS, parses them using IPLD codecs, and follows links.
package implicit

import (
	"context"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/resolver/implicit/codec"
	"github.com/dewebprotocol/malt/core/resolver/implicit/dagcbor"
	"github.com/dewebprotocol/malt/core/resolver/implicit/dagjson"
	"github.com/dewebprotocol/malt/core/resolver/implicit/unixfs"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/key"
	cid "github.com/ipfs/go-cid"
)

// Resolver resolves implicit Merkle-DAG links via CAS.
type Resolver struct {
	cas    cas.Client
	codecs *codec.Registry
}

// NewResolver creates a new implicit resolver with default codecs.
func NewResolver(c cas.Client) *Resolver {
	registry := codec.NewRegistry()
	registry.Register(dagcbor.New())
	registry.Register(dagjson.New())
	registry.Register(unixfs.New())

	return &Resolver{
		cas:    c,
		codecs: registry,
	}
}

// NewResolverWithCodecs creates a resolver with custom codecs.
func NewResolverWithCodecs(c cas.Client, registry *codec.Registry) *Resolver {
	return &Resolver{
		cas:    c,
		codecs: registry,
	}
}

// Resolve resolves a path through Merkle-DAG links.
// It fetches blocks from CAS, parses them, and follows links until the path is consumed.
func (r *Resolver) Resolve(root key.Key, path string) (matchedPath string, target key.Key, ev evidence.Evidence, err error) {
	if root == nil {
		return "", nil, nil, fmt.Errorf("root is nil")
	}

	// Only handle PayloadCID
	if root.Kind() != key.KeyKindPayloadCID {
		return "", nil, nil, fmt.Errorf("implicit resolver only handles PayloadCID, got %v", root.Kind())
	}

	if r.cas == nil {
		return "", nil, nil, fmt.Errorf("CAS client is nil")
	}

	// Get the CID from the key
	payloadCID := root.(*key.PayloadCID)
	c := payloadCID.CID()

	// Track all block content for evidence
	var allBlocks [][]byte

	// Traverse the DAG
	remainingPath := path
	currentCID := c

	for remainingPath != "" {
		// Fetch block from CAS
		blockKey := key.NewPayloadCIDFromCID(currentCID)

		blockContent, err := r.cas.Get(context.Background(), blockKey)
		if err != nil {
			return "", nil, nil, fmt.Errorf("failed to fetch block %s: %w", currentCID, err)
		}
		allBlocks = append(allBlocks, blockContent)

		// Get codec for this CID
		codecName := codecFromCID(currentCID)
		codecImpl, err := r.codecs.Get(codecName)
		if err != nil {
			// Unknown codec, stop here
			targetKey := key.NewPayloadCIDFromCID(currentCID)
			return path, targetKey, evidence.NewImplicitEvidence(concatBlocks(allBlocks)), nil
		}

		// Parse the block
		node, err := codecImpl.Decode(blockContent)
		if err != nil {
			// Failed to parse, stop here
			targetKey := key.NewPayloadCIDFromCID(currentCID)
			return path, targetKey, evidence.NewImplicitEvidence(concatBlocks(allBlocks)), nil
		}

		// Split remaining path
		segments := splitPath(remainingPath)
		if len(segments) == 0 {
			// No more path to resolve
			targetKey := key.NewPayloadCIDFromCID(currentCID)
			return path, targetKey, evidence.NewImplicitEvidence(concatBlocks(allBlocks)), nil
		}

		// Try to resolve the first segment
		nextCID, newRemaining, err := codec.ResolveLink(node, segments)
		if err != nil {
			// Can't resolve further, stop here
			targetKey := key.NewPayloadCIDFromCID(currentCID)
			consumedLen := len(path) - len(remainingPath)
			return path[:consumedLen], targetKey, evidence.NewImplicitEvidence(concatBlocks(allBlocks)), nil
		}

		// Move to next CID
		currentCID = nextCID
		remainingPath = strings.Join(newRemaining, "/")
	}

	// Path fully consumed
	targetKey := key.NewPayloadCIDFromCID(currentCID)
	return path, targetKey, evidence.NewImplicitEvidence(concatBlocks(allBlocks)), nil
}

// Verify verifies the evidence for an implicit step.
func (r *Resolver) Verify(root key.Key, path string, target key.Key, ev evidence.Evidence) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("evidence is nil")
	}

	implicitEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		return false, fmt.Errorf("expected ImplicitEvidence, got %T", ev)
	}

	// Verify block content hash matches the CID
	blockContent := implicitEv.Bytes()
	computedCID, err := key.NewPayloadCID(blockContent)
	if err != nil {
		return false, fmt.Errorf("failed to compute CID: %w", err)
	}

	// Check if the root matches
	if !root.Equals(computedCID) {
		return false, nil
	}

	return true, nil
}

// codecFromCID returns the codec name from a CID.
func codecFromCID(c cid.Cid) string {
	switch c.Prefix().Codec {
	case cid.DagCBOR:
		return "dag-cbor"
	case cid.DagJSON:
		return "dag-json"
	case cid.DagProtobuf:
		return "unixfs"
	case cid.Raw:
		return "raw"
	default:
		return ""
	}
}

// splitPath splits a path into segments.
func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	if path[0] == '/' {
		path = path[1:]
	}
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

// concatBlocks concatenates all block contents.
func concatBlocks(blocks [][]byte) []byte {
	var total int
	for _, b := range blocks {
		total += len(b)
	}
	result := make([]byte, 0, total)
	for _, b := range blocks {
		result = append(result, b...)
	}
	return result
}

// Ensure Resolver implements the resolver interface
var _ interface {
	Resolve(key.Key, string) (string, key.Key, evidence.Evidence, error)
	Verify(key.Key, string, key.Key, evidence.Evidence) (bool, error)
} = (*Resolver)(nil)