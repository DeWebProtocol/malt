package unixfs

import (
	"context"
	"fmt"
	"math"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// MutationPlan is a layout-produced semantic mutation snapshot for one
// UnixFS node. It is an adapter artifact for gateway-style consumers; the
// gateway remains responsible for owning write execution and publication.
type MutationPlan struct {
	BucketID string
	BaseRoot cid.Cid
	Puts     []MutationPut
}

// MutationPut binds one materialized semantic root to its canonical arc set.
type MutationPut struct {
	Object cid.Cid
	Kind   arcset.Kind
	ArcSet *arcset.CanonicalArcSet
}

// MutationPlanForPath exposes canonical map/list arcsets for the UnixFS node
// already reachable at path. For directories it includes only the terminal
// directory map, not descendant subtrees. For large files it includes the file
// map plus the terminal payload list composed from individual list index reads.
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
		BucketID: l.bucketID,
		BaseRoot: root,
		Puts: []MutationPut{
			{
				Object: nodeRoot,
				Kind:   arcset.KindMap,
				ArcSet: nodeArcSet,
			},
		},
	}

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
	plan.Puts = append(plan.Puts, MutationPut{
		Object: payload,
		Kind:   arcset.KindList,
		ArcSet: listArcSet,
	})
	return plan, nil
}

// MutationPlanForRoot exposes canonical map/list arcsets for the complete
// UnixFS tree rooted at root. Child payloads and maps are emitted before their
// parent directories so the final put is the canonical bucket-head map.
func (l *Layout) MutationPlanForRoot(ctx context.Context, baseRoot cid.Cid, root cid.Cid) (*MutationPlan, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root is undefined")
	}

	plan := &MutationPlan{
		BucketID: l.bucketID,
		BaseRoot: baseRoot,
	}
	if err := l.appendRootMutationPuts(ctx, plan, root); err != nil {
		return nil, err
	}
	return plan, nil
}

func (l *Layout) appendRootMutationPuts(ctx context.Context, plan *MutationPlan, nodeRoot cid.Cid) error {
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
			if err := l.appendRootMutationPuts(ctx, plan, child); err != nil {
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
			listArcSet, err := l.canonicalListArcSet(ctx, payload, chunkCount(info.size, info.chunkSize))
			if err != nil {
				return err
			}
			plan.Puts = append(plan.Puts, MutationPut{
				Object: payload,
				Kind:   arcset.KindList,
				ArcSet: listArcSet,
			})
		}
	default:
		return fmt.Errorf("unsupported unixfs node kind %q", kind)
	}

	nodeArcSet, err := l.canonicalNodeArcSet(ctx, nodeRoot, kind)
	if err != nil {
		return err
	}
	plan.Puts = append(plan.Puts, MutationPut{
		Object: nodeRoot,
		Kind:   arcset.KindMap,
		ArcSet: nodeArcSet,
	})
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
		query, proof, err := l.lists.Prove(ctx, l.bucketID, root, index)
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
