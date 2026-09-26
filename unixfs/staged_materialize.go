package unixfs

import (
	"context"
	"fmt"
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

// StagedRootWriter computes a directory candidate while preserving an existing
// Root descriptor. An undefined base creates a new directory.
type StagedRootWriter interface {
	UpdateStagedRoot(ctx context.Context, previous cid.Cid, bindings map[string]string) (cid.Cid, error)
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

// materializeDirectory writes manifests bottom-up. Flat layouts authenticate
// all paths in the top root; hybrid and rooted layouts also commit child roots.
func materializeDirectory(ctx context.Context, roots StagedRootWriter, blocks StagedBlockStore, node *StagedNode, layout LayoutKind, top bool) (*StagedMaterializeResult, error) {
	if node == nil || node.Kind != StagedKindDirectory {
		return nil, fmt.Errorf("directory materialization requires a directory node")
	}
	base := node.Key
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
	children := make([]DirectoryChild, 0, len(names))
	entries := make([]unixfsmodel.DirectoryEntry, 0, len(names))
	stats := &StagedMaterializeResult{}
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		projected := DirectoryChild{Name: name, Root: child.Key}
		entryType := unixfsmodel.DirectoryEntryTypeFile
		switch child.Kind {
		case StagedKindDirectory:
			result, err := materializeDirectory(ctx, roots, blocks, child, layout, false)
			if err != nil {
				return nil, err
			}
			AddStagedMaterializeStats(stats, result)
			projected.Directory = true
			projected.Root = result.Key
			projected.Manifest = result.Key // Flat children return their manifest.
			projected.Descendants = result.Descendants
			entryType = unixfsmodel.DirectoryEntryTypeDir
		case StagedKindMapDirectory:
			if layout == LayoutFlatV1 {
				return nil, fmt.Errorf("flat layout cannot retain opaque map directory %q", name)
			}
			projected.Directory = true
			entryType = unixfsmodel.DirectoryEntryTypeDir
		case StagedKindFile:
		default:
			return nil, fmt.Errorf("unsupported staged child kind %q at %q", child.Kind, name)
		}
		if !projected.Root.Defined() {
			return nil, fmt.Errorf("staged child %q has no materialized target", name)
		}
		children = append(children, projected)
		entries = append(entries, unixfsmodel.DirectoryEntry{Name: name, Type: entryType})
	}
	// Return complete descendants even for rooted layouts; only the shared
	// projection decides which of these paths belong to the authenticated map.
	stats.Descendants = ProjectDirectoryBindings(LayoutHybridV1, children)
	if layout != LayoutFlatV1 && !node.Changed && base.Defined() {
		stats.Key = base
		return stats, nil
	}
	manifest, err := unixfsmodel.EncodeDirectoryManifest(entries)
	if err != nil {
		return nil, fmt.Errorf("marshal directory manifest: %w", err)
	}
	payload, err := blocks.PutWithCodec(ctx, manifest.Data, manifest.Codec)
	if err != nil {
		return nil, fmt.Errorf("upload directory manifest: %w", err)
	}
	expected, err := unixfsmodel.NewDirectoryManifestCID(manifest.Data)
	if err != nil {
		return nil, err
	}
	if !payload.Equals(expected) {
		return nil, fmt.Errorf("directory manifest upload returned a different CID")
	}
	stats.ImmutableObjects++
	if layout == LayoutFlatV1 && !top {
		node.Key, node.StorageKind, node.Changed = payload, "raw", false
		stats.Key = payload
		return stats, nil
	}
	if flusher, ok := blocks.(stagedBlockFlusher); ok {
		if err := flusher.Flush(ctx); err != nil {
			return nil, fmt.Errorf("flush directory manifests: %w", err)
		}
	}
	projected := ProjectDirectoryBindings(layout, children)
	bindings := make(map[string]string, len(projected)+1)
	bindings["@payload"] = payload.String()
	for name, target := range projected {
		bindings[name] = target.String()
	}
	root, err := roots.UpdateStagedRoot(ctx, base, bindings)
	if err != nil {
		return nil, err
	}
	node.Key, node.StorageKind, node.Changed = root, "prefix", false
	stats.Key = root
	stats.ArcCount += len(bindings)
	stats.MALTObjects++
	stats.MALTMaps++
	stats.ArcSets++
	stats.Arcs += len(bindings)
	return stats, nil
}
