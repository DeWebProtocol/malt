package unixfs

import (
	"context"
	"fmt"
	"path"
	"slices"

	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
)

// StagedMaterializeResult summarizes materialization of a staged UnixFS tree.
type StagedMaterializeResult struct {
	Key              cid.Cid
	ArcCount         int
	Descendants      map[string]cid.Cid
	ImmutableObjects int
	MALTObjects      int
	MALTMaps         int
	MALTLists        int
	ArcSets          int
	Arcs             int
}

// StagedRootCreator creates a map root from already serialized bindings.
type StagedRootCreator interface {
	CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error)
}

// StagedBlockStore is the block subset needed by staged materialization.
type StagedBlockStore interface {
	Put(ctx context.Context, data []byte) (cid.Cid, error)
	PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error)
}

type stagedBlockFlusher interface {
	Flush(ctx context.Context) error
}

// AddStagedMaterializeStats aggregates staged materialization counters.
func AddStagedMaterializeStats(dst *StagedMaterializeResult, src *StagedMaterializeResult) {
	if dst == nil || src == nil {
		return
	}
	dst.ImmutableObjects += src.ImmutableObjects
	dst.MALTObjects += src.MALTObjects
	dst.MALTMaps += src.MALTMaps
	dst.MALTLists += src.MALTLists
	dst.ArcSets += src.ArcSets
	dst.Arcs += src.Arcs
	dst.ArcCount += src.ArcCount
}

// MaterializeStagedDirectory preserves the historical hybrid-v1 behavior.
// New layout-aware callers should select an implementation with NewLayout.
func MaterializeStagedDirectory(ctx context.Context, roots StagedRootCreator, blocks StagedBlockStore, node *StagedNode) (*StagedMaterializeResult, error) {
	return materializeHybridDirectory(ctx, roots, blocks, node)
}

// materializeHybridDirectory writes the changed portions of a staged UnixFS
// directory tree and returns its map root. Unchanged staged directories keep
// their existing Key while changed directories are committed bottom-up.
func materializeHybridDirectory(ctx context.Context, roots StagedRootCreator, blocks StagedBlockStore, node *StagedNode) (*StagedMaterializeResult, error) {
	if node == nil || node.Kind != StagedKindDirectory {
		return nil, fmt.Errorf("MaterializeStagedDirectory requires a directory node")
	}

	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		segments, err := ParseCanonicalStagedPath(name)
		if err != nil || len(segments) != 1 || segments[0] != name {
			if err == nil {
				err = fmt.Errorf("child name must be one losslessly canonical portable path segment")
			}
			return nil, fmt.Errorf("invalid staged directory child %q: %w", name, err)
		}
		names = append(names, name)
	}
	slices.Sort(names)

	desc := make(map[string]cid.Cid)
	childKeys := make(map[string]cid.Cid, len(node.Children))
	stats := &StagedMaterializeResult{}
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		if child.Kind == StagedKindDirectory {
			mat, err := materializeHybridDirectory(ctx, roots, blocks, child)
			if err != nil {
				return nil, err
			}
			AddStagedMaterializeStats(stats, mat)
			child.Key = mat.Key
			child.Changed = false
			childKeys[name] = mat.Key
			desc[name] = mat.Key
			for rel, childKey := range mat.Descendants {
				desc[path.Join(name, rel)] = childKey
			}
			continue
		}
		childKeys[name] = child.Key
		desc[name] = child.Key
	}

	if !node.Changed && node.Key.Defined() {
		return &StagedMaterializeResult{
			Key:              node.Key,
			ArcCount:         stats.ArcCount,
			Descendants:      desc,
			ImmutableObjects: stats.ImmutableObjects,
			MALTObjects:      stats.MALTObjects,
			MALTMaps:         stats.MALTMaps,
			MALTLists:        stats.MALTLists,
			ArcSets:          stats.ArcSets,
			Arcs:             stats.Arcs,
		}, nil
	}

	manifestEntries := make([]unixfsmodel.DirectoryEntry, 0, len(names))
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		entryType := unixfsmodel.DirectoryEntryTypeFile
		switch child.Kind {
		case StagedKindDirectory, StagedKindMapDirectory:
			entryType = unixfsmodel.DirectoryEntryTypeDir
		case StagedKindFile:
			// Keep file projection independent of whether the target is CAS,
			// List, or a Map-backed application object.
		default:
			return nil, fmt.Errorf("unsupported staged child kind %q at %q", child.Kind, name)
		}
		manifestEntries = append(manifestEntries, unixfsmodel.DirectoryEntry{Name: name, Type: entryType})
	}
	manifestBlock, err := unixfsmodel.EncodeDirectoryManifest(manifestEntries)
	if err != nil {
		return nil, fmt.Errorf("marshal directory manifest: %w", err)
	}
	payloadCID, err := blocks.PutWithCodec(ctx, manifestBlock.Data, manifestBlock.Codec)
	if err != nil {
		return nil, fmt.Errorf("upload directory manifest: %w", err)
	}
	if flusher, ok := blocks.(stagedBlockFlusher); ok {
		if err := flusher.Flush(ctx); err != nil {
			return nil, fmt.Errorf("flush directory manifest: %w", err)
		}
	}

	bindings := unixfsmodel.DirectoryRootBindings(payloadCID, childKeys, desc)
	rootCID, err := roots.CreateStagedRoot(ctx, bindings)
	if err != nil {
		return nil, err
	}
	node.Key = rootCID
	node.Changed = false
	node.StorageKind = "map"
	arcCount := unixfsmodel.CountDefinedBindings(bindings)
	return &StagedMaterializeResult{
		Key:              rootCID,
		ArcCount:         stats.ArcCount + arcCount,
		Descendants:      desc,
		ImmutableObjects: stats.ImmutableObjects + 1,
		MALTObjects:      stats.MALTObjects + 1,
		MALTMaps:         stats.MALTMaps + 1,
		MALTLists:        stats.MALTLists,
		ArcSets:          stats.ArcSets + 1,
		Arcs:             stats.Arcs + arcCount,
	}, nil
}

func materializeFlatDirectory(ctx context.Context, roots StagedRootCreator, blocks StagedBlockStore, node *StagedNode) (*StagedMaterializeResult, error) {
	if node == nil || node.Kind != StagedKindDirectory {
		return nil, fmt.Errorf("flat layout requires a directory node")
	}
	bindings := make(map[string]string)
	descendants := make(map[string]cid.Cid)
	stats := &StagedMaterializeResult{}
	payload, err := materializeFlatManifest(ctx, blocks, node, "", bindings, descendants, stats)
	if err != nil {
		return nil, err
	}
	if flusher, ok := blocks.(stagedBlockFlusher); ok {
		if err := flusher.Flush(ctx); err != nil {
			return nil, fmt.Errorf("flush flat directory manifests: %w", err)
		}
	}
	bindings["@payload"] = payload.String()
	root, err := roots.CreateStagedRoot(ctx, bindings)
	if err != nil {
		return nil, err
	}
	node.Key = root
	node.StorageKind = "map"
	node.Changed = false
	arcCount := unixfsmodel.CountDefinedBindings(bindings)
	stats.Key = root
	stats.ArcCount += arcCount
	stats.Descendants = descendants
	stats.MALTObjects++
	stats.MALTMaps++
	stats.ArcSets++
	stats.Arcs += arcCount
	return stats, nil
}

func materializeFlatManifest(
	ctx context.Context,
	blocks StagedBlockStore,
	node *StagedNode,
	prefix string,
	bindings map[string]string,
	descendants map[string]cid.Cid,
	stats *StagedMaterializeResult,
) (cid.Cid, error) {
	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		segments, err := ParseCanonicalStagedPath(name)
		if err != nil || len(segments) != 1 || segments[0] != name {
			if err == nil {
				err = fmt.Errorf("child name must be one losslessly canonical portable path segment")
			}
			return cid.Undef, fmt.Errorf("invalid staged directory child %q: %w", name, err)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	manifestEntries := make([]unixfsmodel.DirectoryEntry, 0, len(names))
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		childPath := path.Join(prefix, name)
		entryType := unixfsmodel.DirectoryEntryTypeFile
		var target cid.Cid
		switch child.Kind {
		case StagedKindDirectory:
			entryType = unixfsmodel.DirectoryEntryTypeDir
			var err error
			target, err = materializeFlatManifest(ctx, blocks, child, childPath, bindings, descendants, stats)
			if err != nil {
				return cid.Undef, err
			}
		case StagedKindMapDirectory:
			return cid.Undef, fmt.Errorf("flat layout cannot retain opaque map directory %q", childPath)
		case StagedKindFile:
			target = child.Key
		default:
			return cid.Undef, fmt.Errorf("unsupported staged child kind %q at %q", child.Kind, childPath)
		}
		if !target.Defined() {
			return cid.Undef, fmt.Errorf("staged child %q has no materialized target", childPath)
		}
		bindings[childPath] = target.String()
		descendants[childPath] = target
		manifestEntries = append(manifestEntries, unixfsmodel.DirectoryEntry{Name: name, Type: entryType})
	}
	manifestBlock, err := unixfsmodel.EncodeDirectoryManifest(manifestEntries)
	if err != nil {
		return cid.Undef, fmt.Errorf("marshal directory manifest: %w", err)
	}
	payload, err := blocks.PutWithCodec(ctx, manifestBlock.Data, manifestBlock.Codec)
	if err != nil {
		return cid.Undef, fmt.Errorf("upload directory manifest: %w", err)
	}
	node.Key = payload
	node.StorageKind = "raw"
	node.Changed = false
	stats.ImmutableObjects++
	return payload, nil
}
