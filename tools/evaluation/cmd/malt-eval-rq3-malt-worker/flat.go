package main

import (
	"context"
	"fmt"
	"slices"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt-core/auth/semantic/mapping/radix"
	cid "github.com/ipfs/go-cid"
)

const (
	flatNamespace     = "rq3-flat"
	flatSentinelCause = "canonical-empty-setup:flat-layout-sentinel"
	flatSentinelJSON  = `{"schema_version":"malt-eval-rq3-flat-layout/v1"}`
)

// flatRootOracle is a worker-local, incrementally materialized Core Map. It
// never consumes Gateway state or responses: every expected post-commit root
// is computed from the frozen before/after values before the Gateway mutation
// is submitted.
type flatRootOracle struct {
	semantic *mappingradix.Map
	root     cid.Cid
}

func newFlatRootOracle(ctx context.Context, initial []gatewaytransport.FlatMapChange) (*flatRootOracle, error) {
	if len(initial) == 0 {
		return nil, fmt.Errorf("flat root oracle requires an initial view")
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		return nil, fmt.Errorf("initialize flat root oracle KZG: %w", err)
	}
	// Non-branching keeps only the current oracle root/node cache. Historical
	// roots are retained by the measured Gateway, not duplicated in worker heap.
	semantic, err := mappingradix.NewMap(scheme, materializermemory.New(false))
	if err != nil {
		return nil, fmt.Errorf("initialize flat root oracle map: %w", err)
	}
	values := make(map[arcset.Path]cid.Cid, len(initial))
	for index, change := range initial {
		if change.Path.IsEmpty() || change.Before.Defined() || !change.After.Defined() {
			return nil, fmt.Errorf("flat root oracle initial change %d is invalid", index)
		}
		if _, duplicate := values[change.Path]; duplicate {
			return nil, fmt.Errorf("flat root oracle initial view repeats %s", change.Path.String())
		}
		values[change.Path] = change.After
	}
	root, err := semantic.Commit(ctx, flatNamespace, mapping.NewViewFromPaths(values))
	if err != nil {
		return nil, fmt.Errorf("commit flat root oracle initial view: %w", err)
	}
	return &flatRootOracle{semantic: semantic, root: root}, nil
}

func (o *flatRootOracle) apply(ctx context.Context, changes []gatewaytransport.FlatMapChange) (cid.Cid, error) {
	if o == nil || o.semantic == nil || !o.root.Defined() {
		return cid.Undef, fmt.Errorf("flat root oracle is not initialized")
	}
	updates := make([]mapping.BatchUpdate, len(changes))
	for index, change := range changes {
		updates[index] = mapping.BatchUpdate{Key: change.Path, OldValue: change.Before, NewValue: change.After}
	}
	next, err := o.semantic.BatchUpdate(ctx, flatNamespace, o.root, updates)
	if err != nil {
		return cid.Undef, fmt.Errorf("apply flat root oracle update: %w", err)
	}
	o.root = next
	return next, nil
}

func verifyFlatGatewayRoot(operation string, expected, observed cid.Cid) error {
	if !expected.Defined() || !observed.Defined() || !expected.Equals(observed) {
		return fmt.Errorf("%s Gateway root differs from the independent per-commit flat oracle", operation)
	}
	return nil
}

func flatSentinelBlock() classifiedBlock {
	return classifiedBlock{
		block:    transport.Block{Codec: cid.DagJSON, Data: []byte(flatSentinelJSON)},
		category: categoryCASMetadata, cause: flatSentinelCause, suffix: "flat-layout-sentinel",
	}
}

func flatSentinelEntry() (blueprintEntry, error) {
	coordinate, err := arcset.NewMapCoordinate("@malt-eval/layout")
	if err != nil {
		return blueprintEntry{}, err
	}
	block := flatSentinelBlock()
	key, err := clientcas.CIDForBlock(block.block)
	if err != nil {
		return blueprintEntry{}, err
	}
	target := arcset.NewCASTarget(key)
	return blueprintEntry{coordinate: coordinate, literal: &target}, nil
}

func flatCoordinate(filePath string, mode bool) (arcset.CanonicalCoordinate, error) {
	prefix := "rq3/files/"
	if mode {
		prefix = "rq3/modes/"
	}
	return arcset.NewMapCoordinate(prefix + filePath)
}

func flatFileEntries(path string, file logicalFile) ([]blueprintEntry, error) {
	coordinate, err := flatCoordinate(path, false)
	if err != nil {
		return nil, err
	}
	payload := file.payload
	if !payload.Defined() {
		var err error
		payload, err = clientcas.CIDForBlock(transport.Block{Codec: cid.Raw, Data: file.data})
		if err != nil {
			return nil, err
		}
	}
	payloadTarget := arcset.NewCASTarget(payload)

	mode, err := modeCASBlock(file.mode)
	if err != nil {
		return nil, err
	}
	modeCID, err := clientcas.CIDForBlock(mode.block)
	if err != nil {
		return nil, err
	}
	modeCoordinate, err := flatCoordinate(path, true)
	if err != nil {
		return nil, err
	}
	modeTarget := arcset.NewCASTarget(modeCID)
	return []blueprintEntry{
		{coordinate: coordinate, literal: &payloadTarget},
		{coordinate: modeCoordinate, literal: &modeTarget},
	}, nil
}

func buildFlatBlueprint(files map[string]logicalFile, chunkBytes uint64) (*hybridBlueprint, error) {
	if chunkBytes == 0 || chunkBytes > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("flat blueprint fixed chunk size is outside host bounds")
	}
	topID := objectLogicalID("flat", "")
	entries := make([]blueprintEntry, 0, 1+2*len(files))
	sentinel, err := flatSentinelEntry()
	if err != nil {
		return nil, err
	}
	entries = append(entries, sentinel)
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		added, err := flatFileEntries(path, files[path])
		if err != nil {
			return nil, fmt.Errorf("flat blueprint file %q: %w", path, err)
		}
		entries = append(entries, added...)
	}
	object := &semanticBlueprint{logicalID: topID, kind: arcset.KindMap, entries: entries}
	if err := finalizeBlueprint(object, nil); err != nil {
		return nil, err
	}
	return &hybridBlueprint{
		topID: topID, objects: map[string]*semanticBlueprint{topID: object}, order: []string{topID},
		manifests:   map[string]classifiedBlock{"@malt-eval/layout": flatSentinelBlock()},
		directories: map[string]*directoryIndex{},
	}, nil
}

func buildFlatBlueprintNext(old *hybridBlueprint, changes []fileChange, chunkBytes uint64) (*hybridBlueprint, error) {
	if old == nil || old.objects[old.topID] == nil || len(changes) == 0 || chunkBytes == 0 {
		return nil, fmt.Errorf("incremental flat blueprint is incomplete")
	}
	entries := make(map[string]blueprintEntry, len(old.objects[old.topID].entries)+2*len(changes))
	for _, entry := range old.objects[old.topID].entries {
		entries[string(entry.coordinate.Bytes())] = entry
	}
	for _, change := range changes {
		pathCoordinate, err := flatCoordinate(change.path, false)
		if err != nil {
			return nil, err
		}
		modeCoordinate, err := flatCoordinate(change.path, true)
		if err != nil {
			return nil, err
		}
		delete(entries, string(pathCoordinate.Bytes()))
		delete(entries, string(modeCoordinate.Bytes()))
		if change.after == nil {
			continue
		}
		added, err := flatFileEntries(change.path, *change.after)
		if err != nil {
			return nil, err
		}
		for _, entry := range added {
			entries[string(entry.coordinate.Bytes())] = entry
		}
	}
	ordered := make([]blueprintEntry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	object := &semanticBlueprint{logicalID: old.topID, kind: arcset.KindMap, entries: ordered}
	if err := finalizeBlueprint(object, nil); err != nil {
		return nil, err
	}
	return &hybridBlueprint{
		topID: old.topID, objects: map[string]*semanticBlueprint{old.topID: object}, order: []string{old.topID},
		manifests: old.manifests, directories: map[string]*directoryIndex{},
	}, nil
}

func (b graphBuilder) buildFlat(ctx context.Context, files map[string]logicalFile) (*hybridGraph, error) {
	if b.scheme == nil || b.store == nil {
		return nil, fmt.Errorf("flat graph builder is incomplete")
	}
	blueprint, err := buildFlatBlueprint(files, b.chunkBytes)
	if err != nil {
		return nil, err
	}
	top := blueprint.objects[blueprint.topID]
	entries := make([]arcset.ArcEntry, len(top.entries))
	values := make(map[arcset.Path]cid.Cid, len(top.entries))
	for index, entry := range top.entries {
		if entry.literal == nil {
			return nil, fmt.Errorf("flat graph contains a non-literal binding")
		}
		entries[index] = arcset.ArcEntry{Coordinate: entry.coordinate, Target: *entry.literal}
		values[arcset.CanonicalizePath(entry.coordinate.String())] = entry.literal.CID()
	}
	canonical, err := arcset.NewCanonicalArcSet(arcset.KindMap, entries)
	if err != nil {
		return nil, err
	}
	semantic, err := mappingradix.NewMap(b.scheme, b.store)
	if err != nil {
		return nil, err
	}
	root, err := semantic.Commit(ctx, flatNamespace, mapping.NewViewFromPaths(values))
	if err != nil {
		return nil, err
	}
	object := &semanticObject{
		logicalID: blueprint.topID, kind: arcset.KindMap, root: root, entries: canonical, refs: map[string]string{},
	}
	return &hybridGraph{
		root: root, topID: blueprint.topID, objects: map[string]*semanticObject{blueprint.topID: object},
		order: []string{blueprint.topID}, manifests: blueprint.manifests, directories: map[string]*directoryIndex{},
	}, nil
}
