// Package clientroot converts durable UnixFS filesystem intent into an
// output-free MALT client-root semantic intent. It owns application projection
// and manifest handling, while malt-core owns root computation and verification.
package clientroot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// BlockStore is the exact immutable-block capability needed to read verified
// old manifests and publish canonical new manifests. Both directions are
// independently checked against their CIDs by Planner.
type BlockStore interface {
	Get(context.Context, cid.Cid) ([]byte, error)
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
}

type Planner struct {
	layout unixfs.LayoutKind
	blocks BlockStore
}

func New(layout unixfs.LayoutKind, blocks BlockStore) (*Planner, error) {
	if _, err := unixfs.NewLayout(layout); err != nil {
		return nil, err
	}
	if blocks == nil {
		return nil, fmt.Errorf("UnixFS client-root planner block store is nil")
	}
	return &Planner{layout: layout, blocks: blocks}, nil
}

type treeNode struct {
	kind          unixfsmodel.DirectoryEntryType
	key           cid.Cid
	manifest      cid.Cid
	children      map[string]*treeNode
	oldObject     *mutation.UpdateObject
	dirty         bool
	manifestDirty bool
	outputID      string
}

type desiredTarget struct {
	literal    *arcset.TargetRef
	outputID   string
	outputKind arcset.TargetKind
}

// Plan implements application/writeback.Planner without importing that
// orchestration package. The verified complete view remains the only authority
// for every before-image.
func (p *Planner) Plan(ctx context.Context, view mutation.UpdateView, operations []journal.Operation) (mutation.SemanticIntent, bool, error) {
	if p == nil || p.blocks == nil {
		return mutation.SemanticIntent{}, false, fmt.Errorf("UnixFS client-root planner is nil")
	}
	if ctx == nil {
		return mutation.SemanticIntent{}, false, fmt.Errorf("UnixFS client-root planner context is nil")
	}
	canonical, err := mutation.NormalizeUpdateView(view)
	if err != nil {
		return mutation.SemanticIntent{}, false, fmt.Errorf("normalize verified UnixFS update view: %w", err)
	}
	if err := validateOperations(canonical.BaseRoot, operations); err != nil {
		return mutation.SemanticIntent{}, false, err
	}
	objectsByRoot := make(map[string]*mutation.UpdateObject, len(canonical.Objects))
	objectsByID := make(map[string]struct{}, len(canonical.Objects))
	for index := range canonical.Objects {
		object := &canonical.Objects[index]
		objectsByRoot[object.Root.KeyString()] = object
		objectsByID[object.ObjectID] = struct{}{}
	}
	top := objectsByRoot[canonical.BaseRoot.KeyString()]
	if top == nil || top.Kind != arcset.KindMap {
		return mutation.SemanticIntent{}, false, fmt.Errorf("UnixFS accepted root is not a Map")
	}

	var root *treeNode
	switch p.layout {
	case unixfs.LayoutFlatV1:
		root, err = p.loadFlat(ctx, top)
	case unixfs.LayoutHybridV1:
		root, err = p.loadHybrid(ctx, top, objectsByRoot, map[string]bool{})
	default:
		err = fmt.Errorf("unsupported UnixFS client-root layout %q", p.layout)
	}
	if err != nil {
		return mutation.SemanticIntent{}, false, err
	}
	if err := applyOperations(root, operations); err != nil {
		return mutation.SemanticIntent{}, false, err
	}
	operationID := batchOperationID(operations)

	var intent mutation.SemanticIntent
	switch p.layout {
	case unixfs.LayoutFlatV1:
		intent, err = p.planFlat(ctx, canonical, root, top, operationID)
	case unixfs.LayoutHybridV1:
		intent, err = p.planHybrid(ctx, canonical, root, objectsByID, operationID)
	}
	if err != nil {
		return mutation.SemanticIntent{}, false, err
	}
	if len(intent.Transitions) == 0 {
		return mutation.SemanticIntent{}, false, nil
	}
	normalized, err := mutation.NormalizeSemanticIntent(canonical, intent)
	if err != nil {
		return mutation.SemanticIntent{}, false, err
	}
	return normalized, true, nil
}

func (p *Planner) loadFlat(ctx context.Context, top *mutation.UpdateObject) (*treeNode, error) {
	entries := entriesByPath(top)
	payload, ok := entries["@payload"]
	if !ok || payload.Kind() != arcset.TargetKindCAS {
		return nil, fmt.Errorf("flat-v1 root has no CAS @payload manifest")
	}
	root := &treeNode{kind: unixfsmodel.DirectoryEntryTypeDir, key: top.Root, manifest: payload.CID(), children: map[string]*treeNode{}, oldObject: top}
	if err := p.loadFlatDirectory(ctx, root, "", entries); err != nil {
		return nil, err
	}
	expected := map[string]arcset.TargetRef{"@payload": arcset.NewCASTarget(root.manifest)}
	collectLiteralBindings(root, "", expected, false)
	if err := requireExactEntries(top, expected); err != nil {
		return nil, fmt.Errorf("flat-v1 root projection: %w", err)
	}
	return root, nil
}

func (p *Planner) loadFlatDirectory(ctx context.Context, node *treeNode, prefix string, rootEntries map[string]arcset.TargetRef) error {
	manifest, err := p.readManifest(ctx, node.manifest)
	if err != nil {
		return fmt.Errorf("read flat-v1 manifest %q: %w", prefix, err)
	}
	for _, entry := range manifest.Entries {
		childPath := entry.Name
		if prefix != "" {
			childPath = path.Join(prefix, entry.Name)
		}
		target, ok := rootEntries[childPath]
		if !ok {
			return fmt.Errorf("flat-v1 manifest declares missing binding %q", childPath)
		}
		kind, err := projectedKind(entry.Type, target)
		if err != nil {
			return fmt.Errorf("flat-v1 path %q: %w", childPath, err)
		}
		child := &treeNode{kind: kind, key: target.CID()}
		if kind == unixfsmodel.DirectoryEntryTypeDir {
			if target.Kind() != arcset.TargetKindCAS {
				return fmt.Errorf("flat-v1 directory %q is not a manifest CAS target", childPath)
			}
			child.manifest = target.CID()
			child.children = map[string]*treeNode{}
			if err := p.loadFlatDirectory(ctx, child, childPath, rootEntries); err != nil {
				return err
			}
		}
		node.children[entry.Name] = child
	}
	return nil
}

func (p *Planner) loadHybrid(ctx context.Context, object *mutation.UpdateObject, objectsByRoot map[string]*mutation.UpdateObject, visiting map[string]bool) (*treeNode, error) {
	if visiting[object.Root.KeyString()] {
		return nil, fmt.Errorf("hybrid-v1 directory projection contains a cycle")
	}
	visiting[object.Root.KeyString()] = true
	defer delete(visiting, object.Root.KeyString())
	entries := entriesByPath(object)
	payload, ok := entries["@payload"]
	if !ok || payload.Kind() != arcset.TargetKindCAS {
		return nil, fmt.Errorf("hybrid-v1 directory %s has no CAS @payload manifest", object.Root)
	}
	manifest, err := p.readManifest(ctx, payload.CID())
	if err != nil {
		return nil, fmt.Errorf("read hybrid-v1 manifest %s: %w", payload.CID(), err)
	}
	node := &treeNode{
		kind: unixfsmodel.DirectoryEntryTypeDir, key: object.Root, manifest: payload.CID(),
		children: map[string]*treeNode{}, oldObject: object,
	}
	for _, entry := range manifest.Entries {
		target, ok := entries[entry.Name]
		if !ok {
			return nil, fmt.Errorf("hybrid-v1 manifest declares missing child %q", entry.Name)
		}
		kind, err := projectedKind(entry.Type, target)
		if err != nil {
			return nil, fmt.Errorf("hybrid-v1 child %q: %w", entry.Name, err)
		}
		if kind == unixfsmodel.DirectoryEntryTypeDir {
			if target.Kind() != arcset.TargetKindMap {
				return nil, fmt.Errorf("hybrid-v1 directory child %q is not a Map target", entry.Name)
			}
			childObject := objectsByRoot[target.CID().KeyString()]
			if childObject == nil || childObject.Kind != arcset.KindMap {
				return nil, fmt.Errorf("hybrid-v1 directory child %q is absent from verified view", entry.Name)
			}
			child, err := p.loadHybrid(ctx, childObject, objectsByRoot, visiting)
			if err != nil {
				return nil, err
			}
			node.children[entry.Name] = child
			continue
		}
		node.children[entry.Name] = &treeNode{kind: unixfsmodel.DirectoryEntryTypeFile, key: target.CID()}
	}
	expected := map[string]arcset.TargetRef{"@payload": arcset.NewCASTarget(node.manifest)}
	collectLiteralBindings(node, "", expected, true)
	if err := requireExactEntries(object, expected); err != nil {
		return nil, fmt.Errorf("hybrid-v1 directory %s projection: %w", object.Root, err)
	}
	return node, nil
}

func (p *Planner) readManifest(ctx context.Context, key cid.Cid) (*unixfsmodel.DirectoryManifest, error) {
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

func (p *Planner) storeManifest(ctx context.Context, node *treeNode) error {
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
	stored, err := p.blocks.PutWithCodec(ctx, block.Data, block.Codec)
	if err != nil {
		return err
	}
	if !stored.Equals(expected) {
		return fmt.Errorf("manifest store substituted CID %s for %s", stored, expected)
	}
	node.manifest = expected
	return nil
}

func (p *Planner) planFlat(ctx context.Context, view mutation.UpdateView, root *treeNode, top *mutation.UpdateObject, operationID string) (mutation.SemanticIntent, error) {
	if err := p.prepareFlatManifests(ctx, root); err != nil {
		return mutation.SemanticIntent{}, err
	}
	desired := map[string]desiredTarget{"@payload": literalTarget(arcset.NewCASTarget(root.manifest))}
	collectDesiredBindings(root, "", desired, false)
	changes, err := changesFor(top, desired)
	if err != nil {
		return mutation.SemanticIntent{}, err
	}
	if len(changes) == 0 {
		return mutation.SemanticIntent{}, nil
	}
	transitionID := stableID("unixfs-flat", operationID)
	return mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: view.BaseRoot, TopOutputID: transitionID,
		Transitions: []mutation.IntentTransition{{
			ID: transitionID, ObjectID: top.ObjectID, OldRoot: top.Root, Kind: arcset.KindMap,
			Backend: maltcid.BackendKindOf(top.Root), Changes: changes,
		}},
	}, nil
}

func (p *Planner) prepareFlatManifests(ctx context.Context, node *treeNode) error {
	for _, name := range sortedChildNames(node) {
		child := node.children[name]
		if child.kind == unixfsmodel.DirectoryEntryTypeDir {
			if err := p.prepareFlatManifests(ctx, child); err != nil {
				return err
			}
		}
	}
	if err := p.storeManifest(ctx, node); err != nil {
		return err
	}
	if node.oldObject == nil {
		node.key = node.manifest
	}
	return nil
}

func (p *Planner) planHybrid(ctx context.Context, view mutation.UpdateView, root *treeNode, objectIDs map[string]struct{}, operationID string) (mutation.SemanticIntent, error) {
	topBackend := maltcid.BackendKindOf(view.BaseRoot)
	transitions := make([]mutation.IntentTransition, 0)
	if err := p.buildHybridTransitions(ctx, root, "", topBackend, operationID, objectIDs, &transitions); err != nil {
		return mutation.SemanticIntent{}, err
	}
	if root.outputID == "" {
		return mutation.SemanticIntent{}, nil
	}
	uses := make(map[string]uint32, len(transitions))
	for _, transition := range transitions {
		for _, change := range transition.Changes {
			if change.OutputID != "" {
				uses[change.OutputID]++
			}
		}
	}
	for index := range transitions {
		if transitions[index].ID != root.outputID {
			transitions[index].ExpectedUses = uses[transitions[index].ID]
			if transitions[index].ExpectedUses == 0 {
				return mutation.SemanticIntent{}, fmt.Errorf("UnixFS directory output %q is orphaned", transitions[index].ID)
			}
		}
	}
	return mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: view.BaseRoot,
		Transitions: transitions, TopOutputID: root.outputID,
	}, nil
}

func (p *Planner) buildHybridTransitions(ctx context.Context, node *treeNode, nodePath string, backend maltcid.BackendKind, operationID string, objectIDs map[string]struct{}, transitions *[]mutation.IntentTransition) error {
	for _, name := range sortedChildNames(node) {
		child := node.children[name]
		if child.kind != unixfsmodel.DirectoryEntryTypeDir || !child.dirty {
			continue
		}
		childPath := name
		if nodePath != "" {
			childPath = path.Join(nodePath, name)
		}
		if err := p.buildHybridTransitions(ctx, child, childPath, backend, operationID, objectIDs, transitions); err != nil {
			return err
		}
	}
	if !node.dirty {
		return nil
	}
	if err := p.storeManifest(ctx, node); err != nil {
		return fmt.Errorf("store UnixFS manifest %q: %w", nodePath, err)
	}
	desired := map[string]desiredTarget{"@payload": literalTarget(arcset.NewCASTarget(node.manifest))}
	collectDesiredBindings(node, "", desired, true)
	changes, err := changesFor(node.oldObject, desired)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		node.dirty = false
		node.outputID = ""
		return nil
	}
	seed := operationID + "\x00" + nodePath
	transitionID := stableID("unixfs-dir", seed)
	objectID := ""
	oldRoot := cid.Undef
	transitionBackend := backend
	if node.oldObject != nil {
		objectID = node.oldObject.ObjectID
		oldRoot = node.oldObject.Root
		transitionBackend = maltcid.BackendKindOf(oldRoot)
	} else {
		objectID = unusedObjectID(objectIDs, seed)
		objectIDs[objectID] = struct{}{}
	}
	node.outputID = transitionID
	*transitions = append(*transitions, mutation.IntentTransition{
		ID: transitionID, ObjectID: objectID, OldRoot: oldRoot, Kind: arcset.KindMap,
		Backend: transitionBackend, Changes: changes,
	})
	return nil
}

func collectLiteralBindings(node *treeNode, prefix string, out map[string]arcset.TargetRef, hybrid bool) {
	for _, name := range sortedChildNames(node) {
		child := node.children[name]
		childPath := name
		if prefix != "" {
			childPath = path.Join(prefix, name)
		}
		out[childPath] = targetForNode(child, hybrid)
		if child.kind == unixfsmodel.DirectoryEntryTypeDir {
			collectLiteralBindings(child, childPath, out, hybrid)
		}
	}
}

func collectDesiredBindings(node *treeNode, prefix string, out map[string]desiredTarget, hybrid bool) {
	for _, name := range sortedChildNames(node) {
		child := node.children[name]
		childPath := name
		if prefix != "" {
			childPath = path.Join(prefix, name)
		}
		if hybrid && child.kind == unixfsmodel.DirectoryEntryTypeDir && child.outputID != "" {
			out[childPath] = desiredTarget{outputID: child.outputID, outputKind: arcset.TargetKindMap}
		} else {
			out[childPath] = literalTarget(targetForNode(child, hybrid))
		}
		if child.kind == unixfsmodel.DirectoryEntryTypeDir {
			collectDesiredBindings(child, childPath, out, hybrid)
		}
	}
}

func changesFor(old *mutation.UpdateObject, desired map[string]desiredTarget) ([]mutation.IntentChange, error) {
	oldEntries := map[string]arcset.TargetRef{}
	if old != nil {
		oldEntries = entriesByPath(old)
	}
	keys := make([]string, 0, len(oldEntries)+len(desired))
	seen := make(map[string]struct{}, len(oldEntries)+len(desired))
	for key := range oldEntries {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range desired {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(left, right string) int { return bytes.Compare([]byte(left), []byte(right)) })
	changes := make([]mutation.IntentChange, 0, len(keys))
	for _, key := range keys {
		before, beforeOK := oldEntries[key]
		after, afterOK := desired[key]
		if beforeOK && afterOK && after.outputID == "" && after.literal != nil && targetsEqual(before, *after.literal) {
			continue
		}
		coordinate, err := arcset.NewMapCoordinate(key)
		if err != nil {
			return nil, err
		}
		change := mutation.IntentChange{Coordinate: coordinate}
		if beforeOK {
			value := before
			change.Before = &value
		}
		if afterOK {
			if after.outputID != "" {
				change.OutputID = after.outputID
				change.OutputKind = after.outputKind
			} else if after.literal != nil {
				value := *after.literal
				change.After = &value
			}
		}
		changes = append(changes, change)
	}
	return changes, nil
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
		return fmt.Errorf("UnixFS client-root planner operation batch is empty")
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

func projectedKind(declared unixfsmodel.DirectoryEntryType, target arcset.TargetRef) (unixfsmodel.DirectoryEntryType, error) {
	switch declared {
	case unixfsmodel.DirectoryEntryTypeDir:
		return declared, nil
	case unixfsmodel.DirectoryEntryTypeFile:
		return declared, nil
	case unixfsmodel.DirectoryEntryTypeUnknown:
		if target.Kind() == arcset.TargetKindMap {
			return unixfsmodel.DirectoryEntryTypeDir, nil
		}
		return unixfsmodel.DirectoryEntryTypeFile, nil
	default:
		return "", fmt.Errorf("unsupported manifest entry type %q", declared)
	}
}

func entriesByPath(object *mutation.UpdateObject) map[string]arcset.TargetRef {
	out := make(map[string]arcset.TargetRef, object.Entries.Len())
	for _, entry := range object.Entries.Entries() {
		out[entry.Coordinate.String()] = entry.Target
	}
	return out
}

func requireExactEntries(object *mutation.UpdateObject, expected map[string]arcset.TargetRef) error {
	actual := entriesByPath(object)
	if len(actual) != len(expected) {
		return fmt.Errorf("authenticated bindings count %d does not match manifest projection %d", len(actual), len(expected))
	}
	for key, want := range expected {
		got, ok := actual[key]
		if !ok || !targetsEqual(got, want) {
			return fmt.Errorf("authenticated binding %q does not match manifest projection", key)
		}
	}
	return nil
}

func targetForNode(node *treeNode, hybrid bool) arcset.TargetRef {
	if node.kind == unixfsmodel.DirectoryEntryTypeDir {
		if hybrid {
			return arcset.NewMapTarget(node.key)
		}
		return arcset.NewCASTarget(node.manifest)
	}
	switch maltcid.SemanticKindOf(node.key) {
	case maltcid.SemanticKindMap:
		return arcset.NewMapTarget(node.key)
	case maltcid.SemanticKindList:
		return arcset.NewListTarget(node.key)
	default:
		return arcset.NewCASTarget(node.key)
	}
}

func literalTarget(target arcset.TargetRef) desiredTarget {
	return desiredTarget{literal: &target}
}

func targetsEqual(left, right arcset.TargetRef) bool {
	return left.Kind() == right.Kind() && left.CID().Equals(right.CID())
}

func sortedChildNames(node *treeNode) []string {
	names := make([]string, 0, len(node.children))
	for name := range node.children {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func batchOperationID(operations []journal.Operation) string {
	digest := sha256.New()
	for _, operation := range operations {
		_, _ = digest.Write([]byte(operation.OperationID))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(operation.RetryID))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil)[:16])
}

func stableID(prefix, seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func unusedObjectID(existing map[string]struct{}, seed string) string {
	for nonce := uint64(0); ; nonce++ {
		candidate := stableID("unixfs-object", fmt.Sprintf("%s\x00%d", seed, nonce))
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}
