package unixfs

import (
	"context"
	"fmt"
	"path"
	"strings"

	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
)

// StagedPathStat is the runtime-facing subset of a path stat response needed to
// rebuild a staged tree from an existing UnixFS root.
type StagedPathStat struct {
	Kind        string
	StorageKind string
	Key         string
	Payload     string
}

// StagedPathStatter resolves stat data for a root-relative path.
type StagedPathStatter interface {
	StatStagedPath(ctx context.Context, root string, path string) (StagedPathStat, error)
}

// stagedPathSessionFactory lets the verified facade bind one projection to the
// same short-lived block and proof caches used by this tree load. External
// statters do not need to implement it.
type stagedPathSessionFactory interface {
	newStagedPathSession(BlockGetter) StagedPathStatter
}

type stagedLoadBlockCache struct {
	inner  BlockGetter
	blocks map[string][]byte
}

func newStagedLoadBlockCache(inner BlockGetter) *stagedLoadBlockCache {
	return &stagedLoadBlockCache{inner: inner, blocks: make(map[string][]byte)}
}

func (c *stagedLoadBlockCache) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := c.blocks[key.KeyString()]; ok {
		return append([]byte(nil), cached...), nil
	}
	payload, err := c.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	c.blocks[key.KeyString()] = append([]byte(nil), payload...)
	return append([]byte(nil), payload...), nil
}

// LoadStagedCurrentTree rebuilds a staged directory tree from an existing
// UnixFS root. It preserves existing node keys so later materialization can
// rewrite only changed subtrees.
func LoadStagedCurrentTree(ctx context.Context, statter StagedPathStatter, blocks BlockGetter, rootCID string) (*StagedNode, error) {
	cachedBlocks := newStagedLoadBlockCache(blocks)
	if factory, ok := statter.(stagedPathSessionFactory); ok {
		statter = factory.newStagedPathSession(cachedBlocks)
	}
	rootStat, err := statter.StatStagedPath(ctx, rootCID, "")
	if err != nil {
		return nil, err
	}
	if rootStat.Key != rootCID {
		rootCID = rootStat.Key
	}
	if rootStat.Kind != StagedKindDirectory {
		return nil, fmt.Errorf("current root must be directory, got %q", rootStat.Kind)
	}
	return loadStagedCurrentDirRecursive(ctx, statter, cachedBlocks, rootCID, "", rootStat)
}

func loadStagedCurrentDirRecursive(ctx context.Context, statter StagedPathStatter, blocks BlockGetter, root string, currentPath string, stat StagedPathStat) (*StagedNode, error) {
	node := NewStagedDirectory()
	node.Changed = false
	node.StorageKind = stat.StorageKind
	keyCID, err := cid.Decode(stat.Key)
	if err != nil {
		return nil, fmt.Errorf("decode directory key %q: %w", stat.Key, err)
	}
	node.Key = keyCID

	if strings.TrimSpace(stat.Payload) == "" {
		return node, nil
	}
	payloadCID, err := cid.Decode(stat.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode directory payload %q: %w", stat.Payload, err)
	}
	payload, err := blocks.Get(ctx, payloadCID)
	if err != nil {
		return nil, fmt.Errorf("read directory manifest %s: %w", stat.Payload, err)
	}
	computedPayloadCID, err := payloadCID.Prefix().Sum(payload)
	if err != nil {
		return nil, fmt.Errorf("compute directory manifest CID %s: %w", stat.Payload, err)
	}
	if !computedPayloadCID.Equals(payloadCID) {
		return nil, fmt.Errorf("directory manifest bytes do not match authenticated CID %s", stat.Payload)
	}
	manifest, err := unixfsmodel.ParseDirectoryManifest(payloadCID, payload)
	if err != nil {
		return nil, fmt.Errorf("parse directory manifest %s: %w", stat.Payload, err)
	}
	for _, entry := range manifest.Entries {
		childName := entry.Name
		childPath := childName
		if currentPath != "" {
			childPath = path.Join(currentPath, childName)
		}
		childStat, err := statter.StatStagedPath(ctx, root, childPath)
		if err != nil {
			return nil, err
		}
		projectedKind := childStat.Kind
		if entry.Type != unixfsmodel.DirectoryEntryTypeUnknown {
			projectedKind = string(entry.Type)
			if childStat.Kind != projectedKind {
				return nil, fmt.Errorf(
					"authenticated directory manifest declares %q as %q, stat returned %q",
					childPath,
					projectedKind,
					childStat.Kind,
				)
			}
		}
		switch projectedKind {
		case StagedKindDirectory:
			childDir, err := loadStagedCurrentDirRecursive(ctx, statter, blocks, root, childPath, childStat)
			if err != nil {
				return nil, err
			}
			node.Children[childName] = childDir
		case StagedKindFile:
			childKey, err := cid.Decode(childStat.Key)
			if err != nil {
				return nil, fmt.Errorf("decode file key %q: %w", childStat.Key, err)
			}
			node.Children[childName] = &StagedNode{
				Kind:        StagedKindFile,
				StorageKind: childStat.StorageKind,
				Key:         childKey,
				Changed:     false,
			}
		default:
			return nil, fmt.Errorf("unsupported child kind %q at %q", childStat.Kind, childPath)
		}
	}
	return node, nil
}
