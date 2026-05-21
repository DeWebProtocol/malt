package unixfs

import (
	"context"
	"fmt"
	"math"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// MutationPlan is a layout-produced semantic mutation delta for one UnixFS
// change. It is an adapter artifact for gateway-style consumers; the gateway
// materializes the plan, while root publication remains application policy.
type MutationPlan struct {
	BaseRoot cid.Cid
	Deltas   []MutationDelta
}

// MutationDelta binds one semantic root transition to its canonical arc delta.
type MutationDelta struct {
	Object       cid.Cid
	ExpectedRoot cid.Cid
	Kind         arcset.Kind
	Changes      *arcset.CanonicalArcDelta
	FixedList    *FixedListCommit
}

// FixedListCommit carries the measured fixed-width list commit profile needed
// to replay a list root exactly from logical list entries.
type FixedListCommit struct {
	TotalSize uint64
	ChunkSize uint64
}

// MutationPlanForPath exposes creation deltas for the UnixFS node already
// reachable at path. For directories it includes only the terminal directory
// map, not descendant subtrees. For large files it includes the file map plus
// the terminal payload list composed from individual list index reads.
func (l *Layout) MutationPlanForPath(ctx context.Context, root cid.Cid, path string) (*MutationPlan, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root is undefined")
	}

	nodeRoot, _, err := l.resolveNode(ctx, root, path)
	if err != nil {
		return nil, err
	}
	kind, err := l.nodeType(ctx, nodeRoot)
	if err != nil {
		return nil, err
	}

	nodeArcSet, err := l.canonicalNodeArcSet(ctx, nodeRoot, kind)
	if err != nil {
		return nil, err
	}
	plan := &MutationPlan{
		BaseRoot: root,
	}
	nodeDelta, err := mutationDeltaFromArcSets(cid.Undef, nodeRoot, arcset.KindMap, nil, nodeArcSet)
	if err != nil {
		return nil, err
	}
	plan.Deltas = append(plan.Deltas, nodeDelta)

	if kind != typeFile {
		return plan, nil
	}

	payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @payload", ErrNotFound)
	}
	if codec.SemanticKindOf(payload) != codec.SemanticKindList {
		return plan, nil
	}

	info, err := l.fileInfo(ctx, nodeRoot, payload)
	if err != nil {
		return nil, err
	}
	listArcSet, err := l.canonicalListArcSet(ctx, payload, chunkCount(info.size, info.chunkSize))
	if err != nil {
		return nil, err
	}
	fixedList, err := l.fixedListCommitForPayload(ctx, payload, info)
	if err != nil {
		return nil, err
	}
	listDelta, err := mutationDeltaFromArcSets(cid.Undef, payload, arcset.KindList, nil, listArcSet)
	if err != nil {
		return nil, err
	}
	listDelta.FixedList = fixedList
	plan.Deltas = append(plan.Deltas, listDelta)
	return plan, nil
}

// MutationPlanForRoot exposes canonical map/list deltas for the complete UnixFS
// tree transition from baseRoot to root. Child payloads and maps are emitted
// before their parent directories so the final delta is the canonical current
// root map.
func (l *Layout) MutationPlanForRoot(ctx context.Context, baseRoot cid.Cid, root cid.Cid) (*MutationPlan, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root is undefined")
	}

	plan := &MutationPlan{
		BaseRoot: baseRoot,
	}
	if err := l.appendRootMutationDeltas(ctx, plan, baseRoot, root); err != nil {
		return nil, err
	}
	return plan, nil
}

func (l *Layout) appendRootMutationDeltas(ctx context.Context, plan *MutationPlan, oldNodeRoot, nodeRoot cid.Cid) error {
	if oldNodeRoot.Defined() && oldNodeRoot.Equals(nodeRoot) {
		return nil
	}
	if !oldNodeRoot.Defined() {
		return l.appendRootCreationDeltas(ctx, plan, nodeRoot)
	}

	kind, err := l.nodeType(ctx, nodeRoot)
	if err != nil {
		return err
	}
	oldKind, err := l.nodeType(ctx, oldNodeRoot)
	if err != nil {
		return l.appendRootCreationDeltas(ctx, plan, nodeRoot)
	}
	if oldKind != kind {
		return l.appendRootCreationDeltas(ctx, plan, nodeRoot)
	}

	switch kind {
	case typeDirectory:
		oldPayload, _, ok, err := l.lookup(ctx, oldNodeRoot, payloadPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: missing @payload", ErrNotFound)
		}
		oldNames, err := l.loadDirectoryManifest(ctx, oldPayload)
		if err != nil {
			return err
		}
		oldChildren := make(map[string]cid.Cid, len(oldNames))
		for _, name := range oldNames {
			child, _, ok, err := l.lookup(ctx, oldNodeRoot, arcset.CanonicalizePath(name))
			if err != nil {
				return err
			}
			if ok {
				oldChildren[name] = child
			}
		}

		payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: missing @payload", ErrNotFound)
		}
		names, err := l.loadDirectoryManifest(ctx, payload)
		if err != nil {
			return err
		}
		for _, name := range names {
			child, _, ok, err := l.lookup(ctx, nodeRoot, arcset.CanonicalizePath(name))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: missing directory entry %s", ErrNotFound, name)
			}
			if err := l.appendRootMutationDeltas(ctx, plan, oldChildren[name], child); err != nil {
				return err
			}
		}
	case typeFile:
		oldPayload, _, oldPayloadOK, err := l.lookup(ctx, oldNodeRoot, payloadPath)
		if err != nil {
			return err
		}
		var oldInfo *fileInfo
		if oldPayloadOK && codec.SemanticKindOf(oldPayload) == codec.SemanticKindList {
			oldInfo, err = l.fileInfo(ctx, oldNodeRoot, oldPayload)
			if err != nil {
				return err
			}
		}
		payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: missing @payload", ErrNotFound)
		}
		if codec.SemanticKindOf(payload) == codec.SemanticKindList {
			info, err := l.fileInfo(ctx, nodeRoot, payload)
			if err != nil {
				return err
			}
			if err := l.appendListMutationDelta(ctx, plan, oldPayload, oldInfo, payload, info); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported unixfs node kind %q", kind)
	}

	oldNodeArcSet, err := l.canonicalNodeArcSet(ctx, oldNodeRoot, oldKind)
	if err != nil {
		return err
	}
	nodeArcSet, err := l.canonicalNodeArcSet(ctx, nodeRoot, kind)
	if err != nil {
		return err
	}
	delta, err := mutationDeltaFromArcSets(oldNodeRoot, nodeRoot, arcset.KindMap, oldNodeArcSet, nodeArcSet)
	if err != nil {
		return err
	}
	if delta.Changes != nil {
		plan.Deltas = append(plan.Deltas, delta)
	}
	return nil
}

func (l *Layout) appendRootCreationDeltas(ctx context.Context, plan *MutationPlan, nodeRoot cid.Cid) error {
	kind, err := l.nodeType(ctx, nodeRoot)
	if err != nil {
		return err
	}
	switch kind {
	case typeDirectory:
		payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: missing @payload", ErrNotFound)
		}
		names, err := l.loadDirectoryManifest(ctx, payload)
		if err != nil {
			return err
		}
		for _, name := range names {
			child, _, ok, err := l.lookup(ctx, nodeRoot, arcset.CanonicalizePath(name))
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: missing directory entry %s", ErrNotFound, name)
			}
			if err := l.appendRootCreationDeltas(ctx, plan, child); err != nil {
				return err
			}
		}
	case typeFile:
		payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: missing @payload", ErrNotFound)
		}
		if codec.SemanticKindOf(payload) == codec.SemanticKindList {
			info, err := l.fileInfo(ctx, nodeRoot, payload)
			if err != nil {
				return err
			}
			if err := l.appendListMutationDelta(ctx, plan, cid.Undef, nil, payload, info); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported unixfs node kind %q", kind)
	}

	nodeArcSet, err := l.canonicalNodeArcSet(ctx, nodeRoot, kind)
	if err != nil {
		return err
	}
	delta, err := mutationDeltaFromArcSets(cid.Undef, nodeRoot, arcset.KindMap, nil, nodeArcSet)
	if err != nil {
		return err
	}
	plan.Deltas = append(plan.Deltas, delta)
	return nil
}

func (l *Layout) canonicalNodeArcSet(ctx context.Context, nodeRoot cid.Cid, kind string) (*arcset.CanonicalArcSet, error) {
	typeCID, _, ok, err := l.lookup(ctx, nodeRoot, typePath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @type", ErrNotFound)
	}
	payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @payload", ErrNotFound)
	}

	typeEntry, err := mapEntry("@type", arcset.NewCASTarget(typeCID))
	if err != nil {
		return nil, err
	}
	payloadEntry, err := mapEntry("@payload", arcset.NewTargetRef(payloadTargetKind(payload), payload))
	if err != nil {
		return nil, err
	}
	entries := []arcset.ArcEntry{typeEntry, payloadEntry}

	switch kind {
	case typeFile:
		sizeCID, _, ok, err := l.lookup(ctx, nodeRoot, sizePath)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: missing @size", ErrNotFound)
		}
		chunkSizeCID, _, ok, err := l.lookup(ctx, nodeRoot, chunkSizePath)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("%w: missing @chunksize", ErrNotFound)
		}
		sizeEntry, err := mapEntry("@size", arcset.NewCASTarget(sizeCID))
		if err != nil {
			return nil, err
		}
		chunkSizeEntry, err := mapEntry("@chunksize", arcset.NewCASTarget(chunkSizeCID))
		if err != nil {
			return nil, err
		}
		entries = append(entries, sizeEntry, chunkSizeEntry)
	case typeDirectory:
		names, err := l.loadDirectoryManifest(ctx, payload)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			child, _, ok, err := l.lookup(ctx, nodeRoot, arcset.CanonicalizePath(name))
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("%w: missing directory entry %s", ErrNotFound, name)
			}
			entry, err := mapEntry(name, arcset.NewMapTarget(child))
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
	default:
		return nil, fmt.Errorf("unsupported unixfs node kind %q", kind)
	}

	return arcset.NewCanonicalArcSet(arcset.KindMap, entries)
}

func (l *Layout) canonicalListArcSet(ctx context.Context, root cid.Cid, length uint64) (*arcset.CanonicalArcSet, error) {
	entries := make([]arcset.ArcEntry, 0, length)
	for index := uint64(0); index < length; index++ {
		if index > math.MaxInt64 {
			return nil, fmt.Errorf("list index %d exceeds canonical coordinate range", index)
		}
		query, proof, err := l.lists.Prove(ctx, l.namespace, root, index)
		if err != nil {
			return nil, err
		}
		ok, err := l.lists.Verify(root, index, query, proof)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("list proof failed at index %d", index)
		}
		if query.Length != length {
			return nil, fmt.Errorf("list length = %d, want %d", query.Length, length)
		}
		if !query.Key.Defined() {
			return nil, fmt.Errorf("%w: missing chunk %d", ErrNotFound, index)
		}

		coord, err := arcset.NewListCoordinate(int64(index))
		if err != nil {
			return nil, err
		}
		entries = append(entries, arcset.ArcEntry{
			Coordinate: coord,
			Target:     arcset.NewCASTarget(query.Key),
		})
	}
	return arcset.NewCanonicalArcSet(arcset.KindList, entries)
}

func (l *Layout) appendListMutationDelta(ctx context.Context, plan *MutationPlan, oldPayload cid.Cid, oldInfo *fileInfo, payload cid.Cid, info *fileInfo) error {
	if oldPayload.Defined() && oldPayload.Equals(payload) {
		return nil
	}

	newLen := chunkCount(info.size, info.chunkSize)
	newArcSet, err := l.canonicalListArcSet(ctx, payload, newLen)
	if err != nil {
		return err
	}
	fixedList, err := l.fixedListCommitForPayload(ctx, payload, info)
	if err != nil {
		return err
	}

	object := cid.Undef
	var oldArcSet *arcset.CanonicalArcSet
	if oldPayload.Defined() && oldInfo != nil && oldInfo.chunkSize == info.chunkSize && canReuseMeasuredListDelta(oldInfo, info) {
		oldLen := chunkCount(oldInfo.size, oldInfo.chunkSize)
		oldArcSet, err = l.canonicalListArcSet(ctx, oldPayload, oldLen)
		if err != nil {
			return err
		}
		object = oldPayload
	}

	delta, err := mutationDeltaFromArcSets(object, payload, arcset.KindList, oldArcSet, newArcSet)
	if err != nil {
		return err
	}
	if delta.Changes == nil {
		return nil
	}
	delta.FixedList = fixedList
	plan.Deltas = append(plan.Deltas, delta)
	return nil
}

func canReuseMeasuredListDelta(oldInfo, newInfo *fileInfo) bool {
	if oldInfo == nil || newInfo == nil || oldInfo.chunkSize != newInfo.chunkSize {
		return false
	}
	oldLen := chunkCount(oldInfo.size, oldInfo.chunkSize)
	newLen := chunkCount(newInfo.size, newInfo.chunkSize)
	switch {
	case newLen == oldLen:
		return oldInfo.size == newInfo.size
	case newLen > oldLen:
		return oldInfo.size == oldLen*oldInfo.chunkSize
	default:
		return false
	}
}

func (l *Layout) fixedListCommitForPayload(ctx context.Context, payload cid.Cid, info *fileInfo) (*FixedListCommit, error) {
	measured, ok := l.lists.(list.MeasuredSemantics)
	if !ok {
		return nil, nil
	}
	end := uint64(0)
	result, _, err := measured.ProveRange(ctx, l.namespace, payload, 0, &end)
	if err != nil {
		return nil, nil
	}
	if result.Metadata.ChildCount != chunkCount(info.size, info.chunkSize) {
		return nil, fmt.Errorf("fixed list child count = %d, want %d", result.Metadata.ChildCount, chunkCount(info.size, info.chunkSize))
	}
	if result.Metadata.TotalSize != info.size {
		return nil, fmt.Errorf("fixed list total size = %d, want %d", result.Metadata.TotalSize, info.size)
	}
	if result.Metadata.ChunkSize != info.chunkSize {
		return nil, fmt.Errorf("fixed list chunk size = %d, want %d", result.Metadata.ChunkSize, info.chunkSize)
	}
	return &FixedListCommit{
		TotalSize: result.Metadata.TotalSize,
		ChunkSize: result.Metadata.ChunkSize,
	}, nil
}

func mutationDeltaFromArcSets(object, expectedRoot cid.Cid, kind arcset.Kind, before, after *arcset.CanonicalArcSet) (MutationDelta, error) {
	changes, err := canonicalArcSetChanges(kind, before, after)
	if err != nil {
		return MutationDelta{}, err
	}
	out := MutationDelta{
		Object:       object,
		ExpectedRoot: expectedRoot,
		Kind:         kind,
	}
	if len(changes) == 0 {
		return out, nil
	}
	delta, err := arcset.NewCanonicalArcDelta(kind, changes)
	if err != nil {
		return MutationDelta{}, err
	}
	out.Changes = delta
	return out, nil
}

func canonicalArcSetChanges(kind arcset.Kind, before, after *arcset.CanonicalArcSet) ([]arcset.ArcChange, error) {
	if before != nil && before.Kind() != kind {
		return nil, fmt.Errorf("before arcset kind = %q, want %q", before.Kind(), kind)
	}
	if after != nil && after.Kind() != kind {
		return nil, fmt.Errorf("after arcset kind = %q, want %q", after.Kind(), kind)
	}

	beforeEntries := canonicalEntriesByCoordinate(before)
	afterEntries := canonicalEntriesByCoordinate(after)
	seen := make(map[string]struct{}, len(beforeEntries)+len(afterEntries))
	changes := make([]arcset.ArcChange, 0)

	for key, beforeEntry := range beforeEntries {
		seen[key] = struct{}{}
		afterEntry, ok := afterEntries[key]
		if !ok {
			beforeTarget := beforeEntry.Target
			changes = append(changes, arcset.ArcChange{
				Coordinate: beforeEntry.Coordinate,
				Before:     &beforeTarget,
			})
			continue
		}
		if targetRefEqual(beforeEntry.Target, afterEntry.Target) {
			continue
		}
		beforeTarget := beforeEntry.Target
		afterTarget := afterEntry.Target
		changes = append(changes, arcset.ArcChange{
			Coordinate: beforeEntry.Coordinate,
			Before:     &beforeTarget,
			After:      &afterTarget,
		})
	}
	for key, afterEntry := range afterEntries {
		if _, ok := seen[key]; ok {
			continue
		}
		afterTarget := afterEntry.Target
		changes = append(changes, arcset.ArcChange{
			Coordinate: afterEntry.Coordinate,
			After:      &afterTarget,
		})
	}
	return changes, nil
}

func canonicalEntriesByCoordinate(set *arcset.CanonicalArcSet) map[string]arcset.ArcEntry {
	if set == nil {
		return nil
	}
	entries := set.Entries()
	out := make(map[string]arcset.ArcEntry, len(entries))
	for _, entry := range entries {
		out[entry.Coordinate.String()] = entry
	}
	return out
}

func targetRefEqual(a, b arcset.TargetRef) bool {
	if a.Kind() != b.Kind() {
		return false
	}
	if !a.CID().Defined() && !b.CID().Defined() {
		return true
	}
	return a.CID().Equals(b.CID())
}

func mapEntry(key string, target arcset.TargetRef) (arcset.ArcEntry, error) {
	coord, err := arcset.NewMapCoordinate(key)
	if err != nil {
		return arcset.ArcEntry{}, err
	}
	return arcset.ArcEntry{
		Coordinate: coord,
		Target:     target,
	}, nil
}

func payloadTargetKind(payload cid.Cid) arcset.TargetKind {
	switch codec.SemanticKindOf(payload) {
	case codec.SemanticKindMap:
		return arcset.TargetKindMap
	case codec.SemanticKindList:
		return arcset.TargetKindList
	default:
		return arcset.TargetKindCAS
	}
}

func chunkCount(size, chunkSize uint64) uint64 {
	if size == 0 {
		return 0
	}
	return ((size - 1) / chunkSize) + 1
}
