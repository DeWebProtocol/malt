// Package planner projects durable UnixFS intent into typed authentication
// candidates. It owns filesystem projection and manifest binding; malt-core
// owns input interpretation, authentication trees and candidate verification.
package planner

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
)

// BlockStore is the exact immutable-block capability needed to read verified
// old manifests and publish canonical new manifests. Both directions are
// independently checked against their CIDs by Planner.
type BlockStore interface {
	Get(context.Context, cid.Cid) ([]byte, error)
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
}

type manifestStore struct {
	blocks BlockStore
}

type treeNode struct {
	kind          unixfsmodel.DirectoryEntryType
	key           cid.Cid
	manifest      cid.Cid
	children      map[string]*treeNode
	dirty         bool
	manifestDirty bool
}

func (p *manifestStore) readManifest(ctx context.Context, key cid.Cid) (*unixfsmodel.DirectoryManifest, error) {
	if !key.Defined() {
		return nil, fmt.Errorf("directory manifest CID is undefined")
	}
	body, err := p.blocks.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	computed, err := key.Prefix().Sum(body)
	if err != nil {
		return nil, fmt.Errorf("compute directory manifest CID: %w", err)
	}
	if !computed.Equals(key) {
		return nil, fmt.Errorf("directory manifest bytes do not match CID %s", key)
	}
	return unixfsmodel.ParseDirectoryManifest(key, body)
}

func (p *manifestStore) storeManifest(ctx context.Context, node *treeNode) error {
	if !node.manifestDirty {
		return nil
	}
	names := sortedChildNames(node)
	entries := make([]unixfsmodel.DirectoryEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, unixfsmodel.DirectoryEntry{Name: name, Type: node.children[name].kind})
	}
	block, err := unixfsmodel.EncodeDirectoryManifest(entries)
	if err != nil {
		return err
	}
	expected, err := unixfsmodel.NewDirectoryManifestCID(block.Data)
	if err != nil {
		return err
	}
	if expected.Equals(node.manifest) {
		node.manifestDirty = false
		return nil
	}
	stored, err := p.blocks.PutWithCodec(ctx, block.Data, block.Codec)
	if err != nil {
		return err
	}
	if !stored.Equals(expected) {
		return fmt.Errorf("manifest store substituted CID %s for %s", stored, expected)
	}
	node.manifest = expected
	node.manifestDirty = false
	return nil
}

func applyOperations(root *treeNode, operations []journal.Operation) error {
	for _, operation := range operations {
		segments, err := unixfs.ParseCanonicalStagedPath(operation.Path)
		if err != nil || len(segments) == 0 {
			return fmt.Errorf("filesystem operation %s has invalid path: %w", operation.OperationID, err)
		}
		switch operation.Kind {
		case journal.KindWrite:
			payload, err := cid.Parse(operation.PayloadCID)
			if err != nil || payload.Prefix().Codec != cid.Raw {
				return fmt.Errorf("filesystem write %s has invalid raw payload CID", operation.OperationID)
			}
			parent, ancestors, err := directoryAt(root, segments[:len(segments)-1])
			if err != nil {
				return err
			}
			name := segments[len(segments)-1]
			existing := parent.children[name]
			if existing != nil && existing.kind == unixfsmodel.DirectoryEntryTypeDir {
				return fmt.Errorf("filesystem write %q replaces a directory", operation.Path)
			}
			parent.children[name] = &treeNode{kind: unixfsmodel.DirectoryEntryTypeFile, key: payload}
			markDirty(ancestors, existing == nil)
		case journal.KindMkdir:
			parent, ancestors, err := directoryAt(root, segments[:len(segments)-1])
			if err != nil {
				return err
			}
			name := segments[len(segments)-1]
			if parent.children[name] != nil {
				return fmt.Errorf("filesystem mkdir %q already exists", operation.Path)
			}
			parent.children[name] = &treeNode{
				kind: unixfsmodel.DirectoryEntryTypeDir, children: map[string]*treeNode{},
				dirty: true, manifestDirty: true,
			}
			markDirty(ancestors, true)
		case journal.KindUnlink:
			parent, ancestors, err := directoryAt(root, segments[:len(segments)-1])
			if err != nil {
				return err
			}
			name := segments[len(segments)-1]
			child := parent.children[name]
			if child == nil {
				return fmt.Errorf("filesystem unlink %q is absent", operation.Path)
			}
			if child.kind == unixfsmodel.DirectoryEntryTypeDir && len(child.children) != 0 {
				return fmt.Errorf("filesystem unlink %q is a non-empty directory", operation.Path)
			}
			delete(parent.children, name)
			markDirty(ancestors, true)
		case journal.KindRename:
			if err := applyRename(root, segments, operation.Destination); err != nil {
				return fmt.Errorf("filesystem rename %s: %w", operation.OperationID, err)
			}
		default:
			return fmt.Errorf("filesystem operation %s has unsupported kind %q", operation.OperationID, operation.Kind)
		}
	}
	return nil
}

func applyRename(root *treeNode, source []string, rawDestination string) error {
	destination, err := unixfs.ParseCanonicalStagedPath(rawDestination)
	if err != nil || len(destination) == 0 {
		return fmt.Errorf("invalid destination: %w", err)
	}
	sourcePath, destinationPath := strings.Join(source, "/"), strings.Join(destination, "/")
	if sourcePath == destinationPath || strings.HasPrefix(destinationPath, sourcePath+"/") {
		return fmt.Errorf("destination is equal to or below source")
	}
	sourceParent, sourceAncestors, err := directoryAt(root, source[:len(source)-1])
	if err != nil {
		return err
	}
	destinationParent, destinationAncestors, err := directoryAt(root, destination[:len(destination)-1])
	if err != nil {
		return err
	}
	sourceName := source[len(source)-1]
	destinationName := destination[len(destination)-1]
	value := sourceParent.children[sourceName]
	if value == nil {
		return fmt.Errorf("source %q is absent", sourcePath)
	}
	if existing := destinationParent.children[destinationName]; existing != nil {
		if existing.kind != value.kind {
			return fmt.Errorf("destination %q has a different kind", destinationPath)
		}
		if existing.kind == unixfsmodel.DirectoryEntryTypeDir && len(existing.children) != 0 {
			return fmt.Errorf("destination %q is a non-empty directory", destinationPath)
		}
	}
	delete(sourceParent.children, sourceName)
	destinationParent.children[destinationName] = value
	markDirty(sourceAncestors, true)
	markDirty(destinationAncestors, true)
	return nil
}

func directoryAt(root *treeNode, segments []string) (*treeNode, []*treeNode, error) {
	current := root
	ancestors := []*treeNode{root}
	for _, segment := range segments {
		child := current.children[segment]
		if child == nil {
			return nil, nil, fmt.Errorf("filesystem directory %q is absent", strings.Join(segments, "/"))
		}
		if child.kind != unixfsmodel.DirectoryEntryTypeDir {
			return nil, nil, fmt.Errorf("filesystem path component %q is not a directory", segment)
		}
		current = child
		ancestors = append(ancestors, current)
	}
	return current, ancestors, nil
}

func markDirty(ancestors []*treeNode, manifest bool) {
	for _, node := range ancestors {
		node.dirty = true
	}
	if manifest && len(ancestors) > 0 {
		ancestors[len(ancestors)-1].manifestDirty = true
	}
}

func validateOperations(base cid.Cid, operations []journal.Operation) error {
	if len(operations) == 0 {
		return fmt.Errorf("UnixFS planner operation batch is empty")
	}
	var dataset, branch string
	var revision uint64
	var epoch uint32
	for index, operation := range operations {
		if index > 0 && operation.Sequence <= operations[index-1].Sequence {
			return fmt.Errorf("UnixFS filesystem operations are not in strict journal order")
		}
		operationBase, err := cid.Parse(operation.BaseRoot)
		if err != nil || !operationBase.Equals(base) {
			return fmt.Errorf("filesystem operation %s has a stale base root", operation.OperationID)
		}
		if operation.Status != journal.StatusPendingUpload && operation.Status != journal.StatusCompleted {
			return fmt.Errorf("filesystem operation %s is not frozen or completed", operation.OperationID)
		}
		if index == 0 {
			dataset, branch, revision, epoch = operation.DatasetID, operation.Branch, operation.BaseRevision, operation.EncryptionEpoch
		} else if operation.DatasetID != dataset || operation.Branch != branch || operation.BaseRevision != revision || operation.EncryptionEpoch != epoch {
			return fmt.Errorf("filesystem operation batch crosses an immutable View")
		}
	}
	return nil
}

func sortedChildNames(node *treeNode) []string {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
