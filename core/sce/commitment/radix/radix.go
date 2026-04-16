package radix

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/kvstore"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const MaxCacheEntries = 1024

type options struct {
	kv kvstore.KVStore
}

type Option func(*options)

func WithKVStore(store kvstore.KVStore) Option {
	return func(o *options) {
		o.kv = store
	}
}

type Scheme struct {
	*commitment.BaseScheme
	kv kvstore.KVStore
}

type singleProof struct {
	Nodes [][]byte `json:"nodes"`
}

type aggregatedProofData struct {
	Proofs [][]byte `json:"proofs"`
}

func NewScheme(opts ...Option) (*Scheme, error) {
	cfg := &options{
		kv: kvmemory.New(),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.kv == nil {
		cfg.kv = kvmemory.New()
	}
	return &Scheme{
		BaseScheme: commitment.NewBaseScheme(commitment.NewCacheManager(MaxCacheEntries)),
		kv:         cfg.kv,
	}, nil
}

func (s *Scheme) Commit(arcs arcset.Snapshot) (cid.Cid, error) {
	if arcs == nil {
		return cid.Undef, fmt.Errorf("arc set is nil")
	}

	ctx := context.Background()
	paths, values := commitment.ExtractSortedPathsValues(arcs)
	entries := make([]leafEntry, 0, len(paths))
	for i, path := range paths {
		canonical := canonicalizePath(path)
		entries = append(entries, leafEntry{
			FullPath:  canonical,
			KeyDigest: digestPath(canonical),
			Target:    values[i],
		})
	}

	rootBuild := buildTree(entries)
	indexEntries := make(map[string]cid.Cid)
	rootNodeCID, err := persistBuildNode(ctx, s.kv, rootBuild, indexEntries)
	if err != nil {
		return cid.Undef, err
	}

	rootCID, err := rootCIDFromNodeCID(rootNodeCID)
	if err != nil {
		return cid.Undef, err
	}
	if err := s.refreshHotIndex(ctx, rootCID, indexEntries); err != nil {
		return cid.Undef, err
	}

	commitmentBytes, err := codec.ExtractCommitment(rootCID)
	if err != nil {
		return cid.Undef, err
	}
	s.BaseScheme.Cache.Set(string(commitmentBytes), &commitment.CacheEntry{
		Paths:  paths,
		Values: values,
	})
	return rootCID, nil
}

func (s *Scheme) Prove(comm cid.Cid, arcs arcset.Snapshot, path string) (cid.Cid, []byte, error) {
	return s.ProveSingle(comm, arcs, path)
}

func (s *Scheme) ProveSingle(comm cid.Cid, arcs arcset.Snapshot, path string) (cid.Cid, []byte, error) {
	if comm.Prefix().Codec != codec.CodecMaltRadix {
		return cid.Undef, nil, fmt.Errorf("not a radix commitment CID: codec=%x", comm.Prefix().Codec)
	}

	ctx := context.Background()
	rootNodeCID, err := s.ensureRootNode(ctx, comm, arcs)
	if err != nil {
		return cid.Undef, nil, err
	}

	canonical := canonicalizePath(path)
	digest := digestPath(canonical)

	nodes, target, err := s.walkProofPath(ctx, comm, rootNodeCID, canonical, digest)
	if err != nil {
		return cid.Undef, nil, err
	}

	proofBytes, err := json.Marshal(singleProof{Nodes: nodes})
	if err != nil {
		return cid.Undef, nil, err
	}
	return target, proofBytes, nil
}

func (s *Scheme) Verify(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	return s.VerifySingle(comm, path, value, proof)
}

func (s *Scheme) VerifySingle(comm cid.Cid, path string, value cid.Cid, proof []byte) (bool, error) {
	if comm.Prefix().Codec != codec.CodecMaltRadix {
		return false, fmt.Errorf("not a radix commitment CID: codec=%x", comm.Prefix().Codec)
	}

	var sp singleProof
	if err := json.Unmarshal(proof, &sp); err != nil {
		return false, err
	}
	if len(sp.Nodes) == 0 {
		return false, nil
	}

	currentCID, err := rootNodeCIDFromCommitment(comm)
	if err != nil {
		return false, err
	}

	canonical := canonicalizePath(path)
	digest := digestPath(canonical)
	depth := 0

	for i, nodeBytes := range sp.Nodes {
		computedCID, err := nodeCIDForBytes(nodeBytes)
		if err != nil {
			return false, err
		}
		if !computedCID.Equals(currentCID) {
			return false, nil
		}

		node, err := deserializeNode(nodeBytes)
		if err != nil {
			return false, err
		}

		switch node.Kind {
		case nodeKindInternal:
			if depth > len(digest)-1 {
				return false, nil
			}
			if len(node.NodePath) != depth {
				return false, nil
			}
			slot := digest[depth]
			childCID, ok := node.Children[slot]
			if !ok {
				return false, nil
			}
			currentCID = childCID
			depth++
		case nodeKindLeaf:
			if i != len(sp.Nodes)-1 {
				return false, nil
			}
			for _, entry := range node.Entries {
				if entry.FullPath == canonical && entry.Target.Equals(value) && entry.KeyDigest == digest {
					return true, nil
				}
			}
			return false, nil
		default:
			return false, fmt.Errorf("unknown node kind: %d", node.Kind)
		}
	}

	return false, nil
}

func (s *Scheme) Update(comm cid.Cid, arcs arcset.Snapshot, path string, oldValue, newValue cid.Cid) (cid.Cid, error) {
	if comm.Prefix().Codec != codec.CodecMaltRadix {
		return cid.Undef, fmt.Errorf("not a radix commitment CID: codec=%x", comm.Prefix().Codec)
	}

	ctx := context.Background()
	rootNodeCID, err := s.ensureRootNode(ctx, comm, arcs)
	if err != nil {
		return cid.Undef, err
	}

	canonical := canonicalizePath(path)
	digest := digestPath(canonical)
	touched := make(map[string]cid.Cid)
	newRootNodeCID, err := s.updateInternal(ctx, rootNodeCID, 0, canonical, digest, oldValue, newValue, touched)
	if err != nil {
		return cid.Undef, err
	}

	newRootCID, err := rootCIDFromNodeCID(newRootNodeCID)
	if err != nil {
		return cid.Undef, err
	}
	if err := s.refreshHotIndex(ctx, newRootCID, touched); err != nil {
		return cid.Undef, err
	}

	if arcs != nil {
		updated := snapshotToMap(arcs)
		if newValue.Defined() {
			updated[canonical] = newValue
		} else {
			delete(updated, canonical)
		}
		paths, values := commitment.ExtractSortedPathsValues(arcset.NewMapFrom(updated))
		commitmentBytes, err := codec.ExtractCommitment(newRootCID)
		if err == nil {
			s.BaseScheme.Cache.Set(string(commitmentBytes), &commitment.CacheEntry{
				Paths:  paths,
				Values: values,
			})
		}
	}

	return newRootCID, nil
}

func (s *Scheme) BatchUpdate(comm cid.Cid, arcs arcset.Snapshot, updates map[string]struct {
	Old cid.Cid
	New cid.Cid
}) (cid.Cid, error) {
	if arcs == nil {
		return cid.Undef, fmt.Errorf("arc set is nil")
	}
	updated := snapshotToMap(arcs)
	for path, change := range updates {
		canonical := canonicalizePath(path)
		if change.New.Defined() {
			updated[canonical] = change.New
		} else {
			delete(updated, canonical)
		}
	}
	return s.Commit(arcset.NewMapFrom(updated))
}

func (s *Scheme) BatchProve(comm cid.Cid, arcs arcset.Snapshot, paths []string) (map[string]arcset.BatchProofEntry, error) {
	return s.BaseScheme.BatchProveImpl(comm, arcs, s, paths)
}

func (s *Scheme) BatchVerify(comm cid.Cid, proofs map[string]arcset.BatchProofEntry) (bool, error) {
	return s.BaseScheme.BatchVerifyImpl(comm, s, proofs)
}

func (s *Scheme) AggregateProve(comm cid.Cid, arcs arcset.Snapshot, paths []string) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}
	targets := make([]cid.Cid, len(paths))
	proofs := make([][]byte, len(paths))
	for i, path := range paths {
		target, proof, err := s.ProveSingle(comm, arcs, path)
		if err != nil {
			return nil, err
		}
		targets[i] = target
		proofs[i] = proof
	}
	proofData, err := json.Marshal(aggregatedProofData{Proofs: proofs})
	if err != nil {
		return nil, err
	}
	return &arcset.AggregatedProof{
		Paths:     paths,
		Targets:   targets,
		ProofData: proofData,
	}, nil
}

func (s *Scheme) AggregateVerify(comm cid.Cid, aggProof *arcset.AggregatedProof) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}

	var payload aggregatedProofData
	if err := json.Unmarshal(aggProof.ProofData, &payload); err != nil {
		return false, err
	}
	if len(payload.Proofs) != len(aggProof.Paths) || len(aggProof.Targets) != len(aggProof.Paths) {
		return false, fmt.Errorf("aggregated proof size mismatch")
	}

	for i, path := range aggProof.Paths {
		ok, err := s.VerifySingle(comm, path, aggProof.Targets[i], payload.Proofs[i])
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (s *Scheme) ensureRootNode(ctx context.Context, root cid.Cid, arcs arcset.Snapshot) (cid.Cid, error) {
	rootNodeCID, err := rootNodeCIDFromCommitment(root)
	if err != nil {
		return cid.Undef, err
	}
	has, err := s.kv.Has(ctx, nodeStoreKey(rootNodeCID))
	if err != nil {
		return cid.Undef, err
	}
	if has {
		return rootNodeCID, nil
	}
	if arcs == nil {
		return cid.Undef, fmt.Errorf("root state missing and no arc snapshot provided")
	}
	rebuiltRoot, err := s.Commit(arcs)
	if err != nil {
		return cid.Undef, err
	}
	if !rebuiltRoot.Equals(root) {
		return cid.Undef, fmt.Errorf("rebuilt root %s does not match expected root %s", rebuiltRoot, root)
	}
	return rootNodeCIDFromCommitment(root)
}

func (s *Scheme) walkProofPath(ctx context.Context, root cid.Cid, rootNodeCID cid.Cid, path string, digest [32]byte) ([][]byte, cid.Cid, error) {
	nodes := make([][]byte, 0, 8)
	currentCID := rootNodeCID
	depth := 0

	for {
		if currentCID.Defined() && depth <= len(digest) {
			if hotCID, err := getHotIndex(ctx, s.kv, root, digest[:depth]); err == nil {
				currentCID = hotCID
			}
		}

		node, nodeBytes, err := loadNode(ctx, s.kv, currentCID)
		if err != nil {
			return nil, cid.Undef, err
		}
		nodes = append(nodes, nodeBytes)

		switch node.Kind {
		case nodeKindInternal:
			slot := digest[depth]
			childCID, ok := node.Children[slot]
			if !ok {
				return nil, cid.Undef, fmt.Errorf("path %s not found", path)
			}
			currentCID = childCID
			depth++
		case nodeKindLeaf:
			for _, entry := range node.Entries {
				if entry.FullPath == path && entry.KeyDigest == digest {
					return nodes, entry.Target, nil
				}
			}
			return nil, cid.Undef, fmt.Errorf("path %s not found", path)
		default:
			return nil, cid.Undef, fmt.Errorf("unknown node kind: %d", node.Kind)
		}
	}
}

func (s *Scheme) updateInternal(ctx context.Context, nodeCID cid.Cid, depth int, path string, digest [32]byte, oldValue, newValue cid.Cid, touched map[string]cid.Cid) (cid.Cid, error) {
	node, _, err := loadNode(ctx, s.kv, nodeCID)
	if err != nil {
		return cid.Undef, err
	}
	if node.Kind != nodeKindInternal {
		return cid.Undef, fmt.Errorf("expected internal node at depth %d", depth)
	}

	next := cloneNode(node)
	slot := digest[depth]
	childCID, ok := next.Children[slot]

	switch {
	case !ok && !oldValue.Defined() && newValue.Defined():
		leafCID, err := s.persistLeaf(ctx, leafEntry{
			FullPath:  path,
			KeyDigest: digest,
			Target:    newValue,
		}, touched)
		if err != nil {
			return cid.Undef, err
		}
		next.Children[slot] = leafCID
	case !ok:
		return cid.Undef, fmt.Errorf("path %s not found", path)
	default:
		childNode, _, err := loadNode(ctx, s.kv, childCID)
		if err != nil {
			return cid.Undef, err
		}
		var newChildCID cid.Cid
		switch childNode.Kind {
		case nodeKindInternal:
			newChildCID, err = s.updateInternal(ctx, childCID, depth+1, path, digest, oldValue, newValue, touched)
		case nodeKindLeaf:
			newChildCID, err = s.updateLeaf(ctx, childCID, depth+1, path, digest, oldValue, newValue, touched)
		default:
			err = fmt.Errorf("unknown child node kind: %d", childNode.Kind)
		}
		if err != nil {
			return cid.Undef, err
		}
		if newChildCID.Defined() {
			next.Children[slot] = newChildCID
		} else {
			delete(next.Children, slot)
		}
	}

	newNodeCID, _, err := putNode(ctx, s.kv, next)
	if err != nil {
		return cid.Undef, err
	}
	touched[string(next.NodePath)] = newNodeCID
	return newNodeCID, nil
}

func (s *Scheme) updateLeaf(ctx context.Context, leafCID cid.Cid, depth int, path string, digest [32]byte, oldValue, newValue cid.Cid, touched map[string]cid.Cid) (cid.Cid, error) {
	leaf, _, err := loadNode(ctx, s.kv, leafCID)
	if err != nil {
		return cid.Undef, err
	}
	if leaf.Kind != nodeKindLeaf {
		return cid.Undef, fmt.Errorf("expected leaf node")
	}

	next := cloneNode(leaf)
	matchIdx := -1
	for i, entry := range next.Entries {
		if entry.FullPath == path {
			matchIdx = i
			break
		}
	}

	switch {
	case matchIdx >= 0 && newValue.Defined():
		if oldValue.Defined() && !next.Entries[matchIdx].Target.Equals(oldValue) {
			return cid.Undef, fmt.Errorf("old value mismatch for path %s", path)
		}
		next.Entries[matchIdx].Target = newValue
		sortLeafEntries(next.Entries)
		newLeafCID, _, err := putNode(ctx, s.kv, next)
		if err != nil {
			return cid.Undef, err
		}
		touched[string(next.NodePath)] = newLeafCID
		return newLeafCID, nil
	case matchIdx >= 0 && !newValue.Defined():
		if oldValue.Defined() && !next.Entries[matchIdx].Target.Equals(oldValue) {
			return cid.Undef, fmt.Errorf("old value mismatch for path %s", path)
		}
		next.Entries = append(next.Entries[:matchIdx], next.Entries[matchIdx+1:]...)
		if len(next.Entries) == 0 {
			return cid.Undef, nil
		}
		sortLeafEntries(next.Entries)
		newLeafCID, _, err := putNode(ctx, s.kv, next)
		if err != nil {
			return cid.Undef, err
		}
		touched[string(next.NodePath)] = newLeafCID
		return newLeafCID, nil
	case matchIdx == -1 && !oldValue.Defined() && newValue.Defined():
		newEntry := leafEntry{
			FullPath:  path,
			KeyDigest: digest,
			Target:    newValue,
		}
		if len(next.Entries) > 0 && next.Entries[0].KeyDigest == digest {
			next.Entries = append(next.Entries, newEntry)
			sortLeafEntries(next.Entries)
			newLeafCID, _, err := putNode(ctx, s.kv, next)
			if err != nil {
				return cid.Undef, err
			}
			touched[string(next.NodePath)] = newLeafCID
			return newLeafCID, nil
		}

		newLeafCID, err := s.persistLeaf(ctx, newEntry, touched)
		if err != nil {
			return cid.Undef, err
		}
		return s.buildPersistedBranch(ctx, depth, leafCID, next.Entries[0].KeyDigest, newLeafCID, digest, touched)
	default:
		return cid.Undef, fmt.Errorf("path %s not found", path)
	}
}

func (s *Scheme) buildPersistedBranch(ctx context.Context, depth int, oldLeafCID cid.Cid, oldDigest [32]byte, newLeafCID cid.Cid, newDigest [32]byte, touched map[string]cid.Cid) (cid.Cid, error) {
	node := newInternalNode(newDigest[:depth])
	oldSlot := oldDigest[depth]
	newSlot := newDigest[depth]
	if oldSlot == newSlot {
		childCID, err := s.buildPersistedBranch(ctx, depth+1, oldLeafCID, oldDigest, newLeafCID, newDigest, touched)
		if err != nil {
			return cid.Undef, err
		}
		node.Children[oldSlot] = childCID
	} else {
		node.Children[oldSlot] = oldLeafCID
		node.Children[newSlot] = newLeafCID
	}
	nodeCID, _, err := putNode(ctx, s.kv, node)
	if err != nil {
		return cid.Undef, err
	}
	touched[string(node.NodePath)] = nodeCID
	return nodeCID, nil
}

func (s *Scheme) persistLeaf(ctx context.Context, entry leafEntry, touched map[string]cid.Cid) (cid.Cid, error) {
	leaf := newLeafNode(entry)
	leafCID, _, err := putNode(ctx, s.kv, leaf)
	if err != nil {
		return cid.Undef, err
	}
	touched[string(leaf.NodePath)] = leafCID
	return leafCID, nil
}

func (s *Scheme) refreshHotIndex(ctx context.Context, root cid.Cid, entries map[string]cid.Cid) error {
	batch := s.kv.Batch()
	for prefix, nodeCID := range entries {
		if err := batch.Put(hotIndexKey(root, []byte(prefix)), nodeCID.Bytes()); err != nil {
			batch.Cancel()
			return err
		}
	}
	return batch.Commit(ctx)
}

func snapshotToMap(arcs arcset.Snapshot) map[string]cid.Cid {
	out := make(map[string]cid.Cid)
	if arcs == nil {
		return out
	}
	it := arcs.Iterate()
	for {
		path, target, ok := it.Next()
		if !ok {
			break
		}
		out[canonicalizePath(path)] = target
	}
	return out
}

func rootCIDFromNodeCID(nodeCID cid.Cid) (cid.Cid, error) {
	digest, err := nodeDigest(nodeCID)
	if err != nil {
		return cid.Undef, err
	}
	return codec.NewRadixCid(digest)
}

func rootNodeCIDFromCommitment(root cid.Cid) (cid.Cid, error) {
	commitmentBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return cid.Undef, err
	}
	mhash, err := mh.Encode(commitmentBytes, mh.SHA2_256)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}
