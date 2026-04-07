// Package implicit implements the Resolver interface for Merkle-DAG traversal.
// It fetches a single block from CAS, parses it using IPLD codecs, and finds the next link.
// Multi-hop traversal is handled by the caller (e.g., Gateway).
//
// For dag-pb blocks, the resolver detects different data structures:
//   - UnixFS: File/directory traversal
//   - HAMT: Hash-based routing for dictionaries
//   - Plain dag-pb: Normal link traversal
package implicit

import (
	"context"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/resolver/hamt"
	"github.com/dewebprotocol/malt/core/resolver/implicit/codec"
	"github.com/dewebprotocol/malt/core/resolver/implicit/dagcbor"
	"github.com/dewebprotocol/malt/core/resolver/implicit/dagjson"
	"github.com/dewebprotocol/malt/core/resolver/implicit/dagpb"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
	multihash "github.com/multiformats/go-multihash"
)

// Resolver resolves a single implicit Merkle-DAG link via CAS.
// It parses one block and returns the first link found in the path.
// Multi-hop traversal should be handled by the caller.
type Resolver struct {
	cas        cas.Client
	codecs     *codec.Registry
	hamtConfig hamt.Config
}

// NewResolver creates a new implicit resolver with default codecs.
func NewResolver(c cas.Client) *Resolver {
	registry := codec.NewRegistry()
	registry.Register(dagcbor.New())
	registry.Register(dagjson.New())
	registry.Register(dagpb.New())

	return &Resolver{
		cas:    c,
		codecs: registry,
		hamtConfig: hamt.Config{
			BitWidth: hamt.DefaultBitWidth,
			HashFunc: hamt.DefaultHashFunc,
			MaxDepth: hamt.DefaultMaxDepth,
		},
	}
}

// NewResolverWithHAMTConfig creates a resolver with custom HAMT configuration.
func NewResolverWithHAMTConfig(c cas.Client, cfg hamt.Config) *Resolver {
	r := NewResolver(c)
	r.hamtConfig = cfg
	return r
}

// NewResolverWithCodecs creates a resolver with custom codecs.
func NewResolverWithCodecs(c cas.Client, registry *codec.Registry) *Resolver {
	return &Resolver{
		cas:    c,
		codecs: registry,
	}
}

// Resolve fetches a block from CAS, parses it, and resolves the first path segment.
// It returns:
//   - matchedPath: the path segment that was resolved (or "" if no link found)
//   - target: the CID of the linked block
//   - evidence: the block content as ImplicitEvidence
//   - err: any error
//
// This resolver only handles a single hop. The caller is responsible for
// continuing traversal if needed.
func (r *Resolver) Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error) {
	if !root.Defined() {
		return "", cid.Cid{}, nil, fmt.Errorf("root is not defined")
	}

	if r.cas == nil {
		return "", cid.Cid{}, nil, fmt.Errorf("CAS client is nil")
	}

	// Fetch block from CAS
	blockContent, err := r.cas.Get(context.Background(), root)
	if err != nil {
		return "", cid.Cid{}, nil, fmt.Errorf("failed to fetch block %s: %w", root, err)
	}

	// Get codec for this CID
	codecName := codecFromCID(root)
	codecImpl, err := r.codecs.Get(codecName)
	if err != nil {
		// Unknown codec, return the block content as evidence
		return "", cid.Cid{}, evidence.NewImplicitEvidence(blockContent), nil
	}

	// Parse the block
	node, err := codecImpl.Decode(blockContent)
	if err != nil {
		// Failed to parse, return the block content as evidence
		return "", cid.Cid{}, evidence.NewImplicitEvidence(blockContent), nil
	}

	// If no path, just return the block content
	if path == "" {
		return "", cid.Cid{}, evidence.NewImplicitEvidence(blockContent), nil
	}

	// Split path into segments
	segments := splitPath(path)
	if len(segments) == 0 {
		return "", cid.Cid{}, evidence.NewImplicitEvidence(blockContent), nil
	}

	// For dag-pb blocks, check if it's a HAMT structure
	if codecName == "dag-pb" {
		hamtResult := r.tryHAMTResolution(root, blockContent, path)
		if hamtResult.isHAMT {
			return hamtResult.matchedPath, hamtResult.target, hamtResult.evidence, hamtResult.err
		}
	}

	// Try to resolve the first segment
	nextCID, remaining, err := codec.ResolveLink(node, segments)
	if err != nil {
		// Can't resolve this segment, return the block content
		return "", cid.Cid{}, evidence.NewImplicitEvidence(blockContent), nil
	}

	// Found a link
	matchedPath = segments[0]
	if len(segments) > 1 && len(remaining) < len(segments)-1 {
		// If some path was consumed, adjust matchedPath
		consumed := len(segments) - len(remaining)
		matchedPath = strings.Join(segments[:consumed], "/")
	}

	return matchedPath, nextCID, evidence.NewImplicitEvidence(blockContent), nil
}

// Verify verifies the evidence for an implicit step.
// It performs two checks:
//  1. Verifies that the block content in evidence hashes to the root CID
//  2. Verifies that the path resolves to the target CID within the block
func (r *Resolver) Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("evidence is nil")
	}

	implicitEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		return false, fmt.Errorf("expected ImplicitEvidence, got %T", ev)
	}

	blockContent := implicitEv.Bytes()

	// Step 1: Verify block content hash matches root CID
	if !verifyBlockHash(root, blockContent) {
		return false, nil
	}

	// Step 2: Verify path -> target mapping within the block
	return r.verifyPathMapping(root, blockContent, path, target)
}

// verifyBlockHash verifies that the block content hashes to the given CID.
func verifyBlockHash(root cid.Cid, blockContent []byte) bool {
	prefix := root.Prefix()

	// Compute multihash of block content using the same hash function
	mhash, err := multihash.Sum(blockContent, prefix.MhType, prefix.MhLength)
	if err != nil {
		return false
	}

	// Build CID with same codec and compare
	computedCID := cid.NewCidV1(prefix.Codec, mhash)
	return root.Equals(computedCID)
}

// verifyPathMapping verifies that path resolves to target within the block.
func (r *Resolver) verifyPathMapping(root cid.Cid, blockContent []byte, path string, target cid.Cid) (bool, error) {
	// Empty path means no traversal, target should equal root
	if path == "" {
		return root.Equals(target), nil
	}

	// Get codec for this CID
	codecName := codecFromCID(root)
	codecImpl, err := r.codecs.Get(codecName)
	if err != nil {
		// Unknown codec, cannot verify path mapping
		return false, fmt.Errorf("unknown codec %s, cannot verify path mapping", codecName)
	}

	// Parse the block
	node, err := codecImpl.Decode(blockContent)
	if err != nil {
		return false, fmt.Errorf("failed to decode block: %w", err)
	}

	// For dag-pb blocks, check if it's a HAMT structure
	if codecName == "dag-pb" {
		if r.isHAMTBlock(blockContent) {
			// HAMT verification requires HAMTEvidence, not ImplicitEvidence
			// Cannot verify HAMT path mapping with ImplicitEvidence alone
			return false, fmt.Errorf("HAMT structure requires HAMTEvidence for verification")
		}
	}

	// Resolve the path within the block
	segments := splitPath(path)
	if len(segments) == 0 {
		return root.Equals(target), nil
	}

	resolvedCID, _, err := codec.ResolveLink(node, segments)
	if err != nil {
		return false, nil
	}

	return resolvedCID.Equals(target), nil
}

// isHAMTBlock checks if a block is a HAMT structure.
func (r *Resolver) isHAMTBlock(blockContent []byte) bool {
	_, err := hamt.ParseNode(blockContent)
	return err == nil
}

// codecFromCID returns the codec name from a CID.
func codecFromCID(c cid.Cid) string {
	switch c.Prefix().Codec {
	case cid.DagCBOR:
		return "dag-cbor"
	case cid.DagJSON:
		return "dag-json"
	case cid.DagProtobuf:
		return "dag-pb"
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

// hamtResult holds the result of HAMT detection and resolution.
type hamtResult struct {
	isHAMT      bool
	matchedPath string
	target      cid.Cid
	evidence    evidence.Evidence
	err         error
}

// tryHAMTResolution attempts to detect and resolve a HAMT structure.
// HAMT nodes are dag-pb blocks where the Data field contains a dag-cbor
// encoded map with "0" (bitfield) and "1" (entries) keys.
func (r *Resolver) tryHAMTResolution(root cid.Cid, blockContent []byte, path string) hamtResult {
	// Try to parse as HAMT node
	_, err := hamt.ParseNode(blockContent)
	if err != nil {
		// Not a HAMT structure, continue with normal resolution
		return hamtResult{isHAMT: false}
	}

	// Successfully parsed as HAMT - this is a HAMT node
	if path == "" {
		// Empty path returns the root with HAMT evidence
		return hamtResult{
			isHAMT:   true,
			target:   root,
			evidence: evidence.NewHAMTEvidence(blockContent),
		}
	}

	// Create a HAMT resolver and resolve the key
	hamtResolver := hamt.NewResolver(r.cas,
		hamt.WithBitWidth(r.hamtConfig.BitWidth),
		hamt.WithHashFunc(r.hamtConfig.HashFunc),
		hamt.WithMaxDepth(r.hamtConfig.MaxDepth),
	)

	matchedPath, target, ev, err := hamtResolver.Resolve(root, path)
	if err != nil {
		return hamtResult{
			isHAMT: true,
			err:    err,
		}
	}

	return hamtResult{
		isHAMT:      true,
		matchedPath: matchedPath,
		target:      target,
		evidence:    ev,
	}
}