// Package eval provides Merkle DAG baseline benchmarks.
// These benchmarks measure the rewrite amplification and metadata costs
// of traditional Merkle DAG structures when performing structural updates.
package eval

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/cas/mock"
	cid "github.com/ipfs/go-cid"
)

// MerkleDAGMetrics captures metrics for Merkle DAG baseline benchmarks.
type MerkleDAGMetrics struct {
	// Tree configuration
	Depth      int // depth of the tree
	Fanout     int // fanout at each level
	TotalNodes int // total number of nodes in the tree
	LeafNodes  int // number of leaf nodes

	// Update metrics
	UpdateDepth          int           // depth of the leaf being updated (from root)
	AncestorsRewritten   int           // number of ancestor nodes that must be rewritten
	RewriteAmp           float64       // rewrite amplification factor (ancestors/leaf)
	MetadataChangedBytes int           // total bytes of metadata changed
	UpdateLatency        time.Duration // time to perform update (including ancestor rewrites)

	// Retrieval metrics
	RetrievalDepth    int           // number of hops to reach leaf
	RetrievalLatency  time.Duration // time to retrieve a leaf
	ProofSize         int           // size of implicit "proof" (all ancestor blocks)
	TotalMetadataSize int           // total metadata bytes along path

	// Storage metrics
	TotalStorageBytes int // total bytes stored in CAS
}

// MerkleDAGTree represents a Merkle DAG tree structure for benchmarking.
// Each node contains links to its children, and the structure is committed
// via embedded hash links (traditional Merkle DAG style).
type MerkleDAGTree struct {
	cas    cas.Client
	root   cid.Cid
	nodes  map[string]*MerkleNode // path -> node
	depth  int
	fanout int
}

// MerkleNode represents a node in the Merkle DAG tree.
type MerkleNode struct {
	Path     string        // path from root to this node
	CID      cid.Cid       // current CID (changes on update)
	Children []*MerkleNode // child nodes (if internal)
	Data     []byte        // payload data (if leaf)
	IsLeaf   bool          // whether this is a leaf node
}

// NewMerkleDAGTree creates a new Merkle DAG tree for benchmarking.
func NewMerkleDAGTree(cas cas.Client, depth, fanout int) *MerkleDAGTree {
	return &MerkleDAGTree{
		cas:    cas,
		nodes:  make(map[string]*MerkleNode),
		depth:  depth,
		fanout: fanout,
	}
}

// Build creates a complete tree structure with the given depth and fanout.
// Returns the root CID and total number of nodes.
func (t *MerkleDAGTree) Build(ctx context.Context) (cid.Cid, int, error) {
	root, err := t.buildNode(ctx, "", 0)
	if err != nil {
		return cid.Cid{}, 0, err
	}
	t.root = root
	return root, len(t.nodes), nil
}

// buildNode recursively builds a node at the given path and depth.
func (t *MerkleDAGTree) buildNode(ctx context.Context, path string, depth int) (cid.Cid, error) {
	node := &MerkleNode{
		Path:   path,
		IsLeaf: depth >= t.depth-1,
	}

	var err error

	if node.IsLeaf {
		// Leaf node: contains data payload
		node.Data = []byte(fmt.Sprintf("leaf-data-%s", path))
		node.CID, err = t.storeLeaf(ctx, node.Data)
		if err != nil {
			return cid.Cid{}, err
		}
	} else {
		// Internal node: contains links to children
		node.Children = make([]*MerkleNode, t.fanout)
		childCIDs := make([]cid.Cid, t.fanout)

		for i := 0; i < t.fanout; i++ {
			childPath := fmt.Sprintf("%s/%d", path, i)
			childCID, err2 := t.buildNode(ctx, childPath, depth+1)
			if err2 != nil {
				return cid.Cid{}, err2
			}
			childCIDs[i] = childCID
			node.Children[i] = t.nodes[childPath]
		}

		node.CID, err = t.storeInternal(ctx, childCIDs)
		if err != nil {
			return cid.Cid{}, err
		}
	}

	t.nodes[path] = node
	return node.CID, nil
}

// storeLeaf stores a leaf node's data and returns its CID.
func (t *MerkleDAGTree) storeLeaf(ctx context.Context, data []byte) (cid.Cid, error) {
	return t.cas.Put(ctx, data)
}

// storeInternal stores an internal node with child links and returns its CID.
// This simulates how Merkle DAG nodes embed child CIDs in their content.
func (t *MerkleDAGTree) storeInternal(ctx context.Context, childCIDs []cid.Cid) (cid.Cid, error) {
	// Encode child CIDs into the node content
	// This simulates the embedded-reference Merkle commitment model
	// where the parent's CID depends on the content which includes child CIDs
	content := encodeInternalNode(childCIDs)
	return t.cas.Put(ctx, content)
}

// encodeInternalNode creates content for an internal node containing child CIDs.
func encodeInternalNode(childCIDs []cid.Cid) []byte {
	// Simple encoding: concatenate child CID bytes
	// In real UnixFS, this would be protobuf with proper structure
	var content []byte
	for _, c := range childCIDs {
		content = append(content, c.Bytes()...)
	}
	return content
}

// UpdateLeaf updates a leaf node's data and measures the rewrite cost.
// In Merkle DAG, this requires rewriting all ancestors along the path.
func (t *MerkleDAGTree) UpdateLeaf(ctx context.Context, leafPath string, newData []byte) (*MerkleDAGMetrics, error) {
	start := time.Now()
	metrics := &MerkleDAGMetrics{
		Depth:  t.depth,
		Fanout: t.fanout,
	}

	// Parse the path to find the leaf and its ancestors
	// Path depth = tree depth - 1 (root has no path segment)
	pathSegments := parsePath(leafPath)
	expectedPathDepth := t.depth - 1
	if len(pathSegments) != expectedPathDepth {
		return nil, fmt.Errorf("path depth mismatch: expected %d, got %d", expectedPathDepth, len(pathSegments))
	}

	metrics.UpdateDepth = expectedPathDepth        // path segments = tree depth - 1
	metrics.AncestorsRewritten = expectedPathDepth // all ancestors must be rewritten

	// Step 1: Update the leaf
	leafNode := t.nodes[leafPath]
	if leafNode == nil {
		return nil, fmt.Errorf("leaf node not found at path %s", leafPath)
	}

	_ = leafNode.CID // old CID is replaced
	leafNode.Data = newData
	newLeafCID, err := t.storeLeaf(ctx, newData)
	if err != nil {
		return nil, err
	}
	leafNode.CID = newLeafCID

	// Step 2: Rewrite all ancestors (bottom-up)
	// This is the key cost of Merkle DAG: ancestor rewrite propagation
	ancestorPaths := getAncestorPaths(leafPath)
	oldAncestorCIDs := make([]cid.Cid, len(ancestorPaths))
	newAncestorCIDs := make([]cid.Cid, len(ancestorPaths))

	for i := len(ancestorPaths) - 1; i >= 0; i-- {
		ancestorPath := ancestorPaths[i]
		ancestorNode := t.nodes[ancestorPath]
		if ancestorNode == nil {
			return nil, fmt.Errorf("ancestor node not found at path %s", ancestorPath)
		}

		oldAncestorCIDs[i] = ancestorNode.CID

		// Find which child link to update
		childIndex := parseChildIndex(ancestorPath, leafPath)
		ancestorNode.Children[childIndex].CID = newLeafCID

		// If we just updated an immediate child, use that CID
		// Otherwise use the CID from the child we just rewrote
		if i == len(ancestorPaths)-1 {
			// This is the parent of the leaf, update with new leaf CID
		} else {
			// This is a higher ancestor, update with the rewritten child's new CID
			childPath := ancestorPaths[i+1]
			ancestorNode.Children[childIndex].CID = t.nodes[childPath].CID
		}

		// Re-encode and store the ancestor
		childCIDs := make([]cid.Cid, len(ancestorNode.Children))
		for j, child := range ancestorNode.Children {
			childCIDs[j] = child.CID
		}

		newCID, err := t.storeInternal(ctx, childCIDs)
		if err != nil {
			return nil, err
		}
		ancestorNode.CID = newCID
		newAncestorCIDs[i] = newCID
	}

	// Update root
	t.root = t.nodes[""].CID

	metrics.UpdateLatency = time.Since(start)
	metrics.RewriteAmp = float64(metrics.AncestorsRewritten + 1) // +1 for the leaf itself

	// Calculate metadata changed
	// Each ancestor rewrite changes its embedded links
	metrics.MetadataChangedBytes = metrics.AncestorsRewritten * (t.fanout * 36) // approximate CID size

	return metrics, nil
}

// RetrieveLeaf retrieves a leaf node and measures retrieval cost.
func (t *MerkleDAGTree) RetrieveLeaf(ctx context.Context, leafPath string) (*MerkleDAGMetrics, error) {
	start := time.Now()
	metrics := &MerkleDAGMetrics{
		Depth:  t.depth,
		Fanout: t.fanout,
	}

	pathSegments := parsePath(leafPath)
	// Retrieval depth = tree depth (number of blocks to fetch from root to leaf)
	// = len(pathSegments) + 1 (root + internal nodes traversed)
	metrics.RetrievalDepth = len(pathSegments) + 1

	// Traverse from root to leaf
	currentCID := t.root
	totalProofSize := 0

	for i, segment := range pathSegments {
		// Fetch current node
		data, err := t.cas.Get(ctx, currentCID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch node at depth %d: %w", i, err)
		}
		totalProofSize += len(data)

		if i == len(pathSegments)-1 {
			// This is the leaf
			break
		}

		// Extract child CID from internal node
		childIndex := mustParseInt(segment)
		childCIDs := decodeInternalNode(data)
		if childIndex >= len(childCIDs) {
			return nil, fmt.Errorf("invalid child index %d at depth %d", childIndex, i)
		}
		currentCID = childCIDs[childIndex]
	}

	metrics.RetrievalLatency = time.Since(start)
	metrics.ProofSize = totalProofSize
	metrics.TotalMetadataSize = metrics.RetrievalDepth * (t.fanout * 36) // approximate

	return metrics, nil
}

// decodeInternalNode extracts child CIDs from internal node content.
func decodeInternalNode(data []byte) []cid.Cid {
	// Each CID is ~36 bytes (version + codec + multihash)
	cidSize := 36
	count := len(data) / cidSize
	cids := make([]cid.Cid, count)

	for i := 0; i < count; i++ {
		cidBytes := data[i*cidSize : (i+1)*cidSize]
		c, err := cid.Cast(cidBytes)
		if err != nil {
			// Skip invalid CIDs
			continue
		}
		cids[i] = c
	}

	return cids
}

// parsePath splits a path into segments.
func parsePath(path string) []string {
	if path == "" {
		return []string{}
	}
	// Remove leading "/" if present
	if path[0] == '/' {
		path = path[1:]
	}
	segments := []string{}
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			segments = append(segments, path[start:i])
			start = i + 1
		}
	}
	if start < len(path) {
		segments = append(segments, path[start:])
	}
	return segments
}

// getAncestorPaths returns all ancestor paths for a given leaf path, from root to parent.
// For leaf path "/0/0/0", returns ["", "/0", "/0/0"] (root, level-1, level-2).
// Note: paths are formatted to match the storage format (with leading "/" for non-root).
func getAncestorPaths(leafPath string) []string {
	segments := parsePath(leafPath)
	// ancestors = root + all internal nodes above leaf
	ancestors := make([]string, len(segments))

	// Root node has empty path
	ancestors[0] = ""

	// Build intermediate paths with leading "/" (matching buildNode format)
	for i := 0; i < len(segments)-1; i++ {
		path := ""
		for j := 0; j <= i; j++ {
			path += "/" + segments[j]
		}
		ancestors[i+1] = path
	}

	return ancestors
}

// parseChildIndex finds which child of an ancestor the leaf belongs to.
func parseChildIndex(ancestorPath, leafPath string) int {
	ancestorSegments := parsePath(ancestorPath)
	leafSegments := parsePath(leafPath)

	if len(leafSegments) <= len(ancestorSegments) {
		return -1
	}

	return mustParseInt(leafSegments[len(ancestorSegments)])
}

// mustParseInt parses an integer string.
func mustParseInt(s string) int {
	var result int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		}
	}
	return result
}

// MerkleDAGBenchmarkRunner runs Merkle DAG baseline benchmarks.
type MerkleDAGBenchmarkRunner struct {
	cas    cas.Client
	depths []int
	fanout int
	seed   int64
}

// NewMerkleDAGBenchmarkRunner creates a new benchmark runner.
func NewMerkleDAGBenchmarkRunner(depths []int, fanout int, seed int64) *MerkleDAGBenchmarkRunner {
	return &MerkleDAGBenchmarkRunner{
		cas:    mock.NewCAS(mock.WithoutLatency()),
		depths: depths,
		fanout: fanout,
		seed:   seed,
	}
}

// RunBaselineBenchmark runs the full baseline benchmark suite.
// Returns metrics comparing Merkle DAG update costs with MALT.
func (r *MerkleDAGBenchmarkRunner) RunBaselineBenchmark(ctx context.Context) (map[int]*MerkleDAGMetrics, error) {
	results := make(map[int]*MerkleDAGMetrics)

	for _, depth := range r.depths {
		metrics, err := r.runDepthBenchmark(ctx, depth)
		if err != nil {
			return nil, fmt.Errorf("benchmark failed for depth %d: %w", depth, err)
		}
		results[depth] = metrics
	}

	return results, nil
}

// runDepthBenchmark runs benchmark for a specific tree depth.
func (r *MerkleDAGBenchmarkRunner) runDepthBenchmark(ctx context.Context, depth int) (*MerkleDAGMetrics, error) {
	// Build tree
	tree := NewMerkleDAGTree(r.cas, depth, r.fanout)
	_, totalNodes, err := tree.Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build tree: %w", err)
	}

	// Calculate leaf nodes
	leafNodes := powInt(r.fanout, depth-1)

	metrics := &MerkleDAGMetrics{
		Depth:      depth,
		Fanout:     r.fanout,
		TotalNodes: totalNodes,
		LeafNodes:  leafNodes,
	}

	// Select a random leaf to update
	rng := rand.New(rand.NewSource(r.seed))
	leafIndex := rng.Intn(leafNodes)
	leafPath := generateLeafPath(leafIndex, depth, r.fanout)

	// Measure retrieval before update
	retrieveMetrics, err := tree.RetrieveLeaf(ctx, leafPath)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve leaf: %w", err)
	}
	metrics.RetrievalDepth = retrieveMetrics.RetrievalDepth
	metrics.RetrievalLatency = retrieveMetrics.RetrievalLatency
	metrics.ProofSize = retrieveMetrics.ProofSize
	metrics.TotalMetadataSize = retrieveMetrics.TotalMetadataSize

	// Measure update cost (the key metric for baseline)
	updateMetrics, err := tree.UpdateLeaf(ctx, leafPath, []byte("updated-data"))
	if err != nil {
		return nil, fmt.Errorf("failed to update leaf: %w", err)
	}
	metrics.AncestorsRewritten = updateMetrics.AncestorsRewritten
	metrics.RewriteAmp = updateMetrics.RewriteAmp
	metrics.MetadataChangedBytes = updateMetrics.MetadataChangedBytes
	metrics.UpdateLatency = updateMetrics.UpdateLatency

	// Calculate total storage
	metrics.TotalStorageBytes = totalNodes * 100 // approximate per-node size

	return metrics, nil
}

// generateLeafPath generates a path to a specific leaf node.
func generateLeafPath(index, depth, fanout int) string {
	segments := make([]string, depth-1)
	for i := depth - 2; i >= 0; i-- {
		segments[i] = fmt.Sprintf("%d", index%fanout)
		index = index / fanout
	}
	return "/" + joinPath(segments)
}

// joinPath joins path segments.
func joinPath(segments []string) string {
	result := ""
	for i, s := range segments {
		if i > 0 {
			result += "/"
		}
		result += s
	}
	return result
}

// powInt computes integer power.
func powInt(base, exp int) int {
	result := 1
	for i := 0; i < exp; i++ {
		result *= base
	}
	return result
}

// CompareMetrics compares Merkle DAG metrics with MALT metrics.
type CompareMetrics struct {
	Depth                int
	MerkleRewriteAmp     float64
	MALTRewriteAmp       float64
	RewriteAmpReduction  float64 // percentage reduction
	MerkleAncestors      int
	MALTUpdates          int // MALT updates are localized (always 1)
	MerkleProofSize      int
	MALTProofSize        int
	MerkleUpdateLatency  time.Duration
	MALTUpdateLatency    time.Duration
	MerkleMetadataChange int
	MALTMetadataChange   int
}

// CompareWithMALT compares Merkle DAG baseline with MALT metrics.
func CompareWithMALT(merkleMetrics map[int]*MerkleDAGMetrics, maltMetrics map[int]*Metrics) map[int]*CompareMetrics {
	comparison := make(map[int]*CompareMetrics)

	for depth, merkle := range merkleMetrics {
		// Find matching MALT metrics (by arc count which approximates tree size)
		arcCount := merkle.LeafNodes
		malt, ok := maltMetrics[arcCount]
		if !ok {
			// Try to find closest match
			for k, v := range maltMetrics {
				if k >= arcCount/2 && k <= arcCount*2 {
					malt = v
					break
				}
			}
		}

		cmp := &CompareMetrics{
			Depth:                depth,
			MerkleRewriteAmp:     merkle.RewriteAmp,
			MALTRewriteAmp:       1.0, // MALT rewrite amp is always 1 (localized)
			MerkleAncestors:      merkle.AncestorsRewritten,
			MALTUpdates:          1,
			MerkleProofSize:      merkle.ProofSize,
			MerkleUpdateLatency:  merkle.UpdateLatency,
			MerkleMetadataChange: merkle.MetadataChangedBytes,
		}

		if malt != nil {
			cmp.MALTProofSize = malt.ProofSize
			cmp.MALTUpdateLatency = malt.UpdateTime
			cmp.MALTMetadataChange = malt.RootSize // MALT only changes the root

			cmp.RewriteAmpReduction = (merkle.RewriteAmp - 1.0) / merkle.RewriteAmp * 100
		}

		comparison[depth] = cmp
	}

	return comparison
}
