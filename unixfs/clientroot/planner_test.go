package clientroot

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt-core/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt-core/mutation"
	clientwriter "github.com/dewebprotocol/malt-core/sdk/writer"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestPlannerComputesVerifiableFlatAndHybridCandidatesAcrossBackends(t *testing.T) {
	backends := []struct {
		name string
		new  func(*testing.T) commitment.IndexCommitment
	}{
		{name: "kzg", new: func(t *testing.T) commitment.IndexCommitment {
			scheme, err := kzg.NewScheme()
			if err != nil {
				t.Fatal(err)
			}
			return scheme
		}},
		{name: "ipa", new: func(t *testing.T) commitment.IndexCommitment {
			scheme, err := ipa.NewCommitterScheme(ipa.ProfileDirect)
			if err != nil {
				t.Fatal(err)
			}
			return scheme
		}},
	}
	for _, backend := range backends {
		for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1} {
			t.Run(backend.name+"/"+string(layout), func(t *testing.T) {
				fixture := newPlannerFixture(t, layout, backend.new(t))
				newPayload := fixture.blocks.putRaw(t, []byte("new body"))
				operations := plannerOperations(fixture.root, newPayload)
				planner, err := New(layout, fixture.blocks)
				if err != nil {
					t.Fatal(err)
				}
				intent, changed, err := planner.Plan(t.Context(), fixture.view, operations)
				if err != nil {
					t.Fatal(err)
				}
				if !changed {
					t.Fatal("mutating filesystem batch was classified as no-change")
				}
				verified, err := fixture.writer.VerifyUpdateView(t.Context(), fixture.view)
				if err != nil {
					t.Fatal(err)
				}
				computed, err := fixture.writer.ComputeBundle(t.Context(), "fs-planner-test", verified, intent)
				if err != nil {
					t.Fatal(err)
				}
				if !computed.Bundle.Candidate.Defined() || computed.Bundle.Candidate.Equals(fixture.root) || !computed.NextView.BaseRoot.Equals(computed.Bundle.Candidate) {
					t.Fatalf("computed candidate=%s next=%s", computed.Bundle.Candidate, computed.NextView.BaseRoot)
				}
				freshStore := materializermemory.New(true)
				fresh, err := clientwriter.NewRuntime(freshStore, map[maltcid.BackendKind]commitment.IndexCommitment{maltcid.BackendKindOf(fixture.root): backend.new(t)})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := fresh.VerifyUpdateView(t.Context(), computed.NextView); err != nil {
					t.Fatalf("next view does not independently verify: %v", err)
				}
				assertPlannedTree(t, planner, layout, computed.NextView)
			})
		}
	}
}

func TestPlannerRejectsManifestCIDSubstitutionAndCorruptOldManifest(t *testing.T) {
	fixture := newPlannerFixture(t, unixfs.LayoutHybridV1, mustKZG(t))
	planner, err := New(unixfs.LayoutHybridV1, fixture.blocks)
	if err != nil {
		t.Fatal(err)
	}
	newPayload := fixture.blocks.putRaw(t, []byte("new"))
	operations := plannerOperations(fixture.root, newPayload)[:1]
	fixture.blocks.substitutePut = plannerRawCID(t, []byte("substituted manifest"))
	if _, _, err := planner.Plan(t.Context(), fixture.view, operations); err == nil {
		t.Fatal("manifest CID substitution was accepted")
	}

	fixture.blocks.substitutePut = cid.Undef
	top := objectByRoot(t, fixture.view, fixture.root)
	manifest := entriesByPath(top)["@payload"].CID()
	fixture.blocks.values[manifest.KeyString()] = []byte("corrupt manifest")
	if _, _, err := planner.Plan(t.Context(), fixture.view, operations); err == nil {
		t.Fatal("corrupt old manifest bytes were accepted")
	}
}

func TestPlannerRejectsPartialViewIdentityAndUnfrozenOperations(t *testing.T) {
	fixture := newPlannerFixture(t, unixfs.LayoutFlatV1, mustKZG(t))
	planner, err := New(unixfs.LayoutFlatV1, fixture.blocks)
	if err != nil {
		t.Fatal(err)
	}
	payload := fixture.blocks.putRaw(t, []byte("new"))
	operations := plannerOperations(fixture.root, payload)
	operations[1].BaseRevision++
	if _, _, err := planner.Plan(t.Context(), fixture.view, operations); err == nil {
		t.Fatal("operation batch crossing immutable Views was accepted")
	}
	operations = plannerOperations(fixture.root, payload)
	operations[0].Status = journal.StatusLocalDirty
	if _, _, err := planner.Plan(t.Context(), fixture.view, operations); err == nil {
		t.Fatal("unfrozen operation was accepted")
	}
}

func TestPlannerClassifiesEquivalentContentAndCanceledNamespaceAsNoChange(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1} {
		t.Run(string(layout), func(t *testing.T) {
			fixture := newPlannerFixture(t, layout, mustKZG(t))
			planner, err := New(layout, fixture.blocks)
			if err != nil {
				t.Fatal(err)
			}
			sameWrite := plannerOperations(fixture.root, fixture.oldPayload)[2:3]
			sameWrite[0].Path = "docs/old.txt"
			if intent, changed, err := planner.Plan(t.Context(), fixture.view, sameWrite); err != nil || changed || len(intent.Transitions) != 0 {
				t.Fatalf("equivalent write intent=%#v changed=%v err=%v", intent, changed, err)
			}

			cancel := plannerOperations(fixture.root, fixture.oldPayload)[:2]
			cancel[0].Path = "temporary"
			cancel[1].Kind = journal.KindUnlink
			cancel[1].Path = "temporary"
			cancel[1].Destination = ""
			if intent, changed, err := planner.Plan(t.Context(), fixture.view, cancel); err != nil || changed || len(intent.Transitions) != 0 {
				t.Fatalf("canceled namespace intent=%#v changed=%v err=%v", intent, changed, err)
			}
		})
	}
}

type plannerFixture struct {
	root       cid.Cid
	view       mutation.UpdateView
	oldPayload cid.Cid
	blocks     *plannerBlocks
	writer     *clientwriter.Runtime
}

func newPlannerFixture(t *testing.T, layoutKind unixfs.LayoutKind, scheme commitment.IndexCommitment) plannerFixture {
	t.Helper()
	store := materializermemory.New(true)
	blocks := &plannerBlocks{values: map[string][]byte{}}
	creator := &plannerRootCreator{t: t, scheme: scheme, store: store}
	oldPayload := blocks.putRaw(t, []byte("old body"))
	deletePayload := blocks.putRaw(t, []byte("delete body"))
	rootNode := unixfs.NewStagedDirectory()
	if err := unixfs.SetStagedFile(rootNode, "docs/old.txt", oldPayload); err != nil {
		t.Fatal(err)
	}
	if err := unixfs.SetStagedFile(rootNode, "docs/delete.txt", deletePayload); err != nil {
		t.Fatal(err)
	}
	layout, err := unixfs.NewLayout(layoutKind)
	if err != nil {
		t.Fatal(err)
	}
	result, err := layout.Materialize(t.Context(), creator, blocks, rootNode)
	if err != nil {
		t.Fatal(err)
	}
	objects := append([]mutation.UpdateObject(nil), creator.objects...)
	view := mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: result.Key, Bounds: mutation.UpdateViewBounds{MaxObjects: 64, MaxTotalEntries: 4096, MaxDepth: 32},
		Objects: objects,
	}
	view, err = mutation.NormalizeUpdateView(view)
	if err != nil {
		t.Fatal(err)
	}
	backend := maltcid.BackendKindOf(result.Key)
	writer, err := clientwriter.NewRuntime(store, map[maltcid.BackendKind]commitment.IndexCommitment{backend: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return plannerFixture{root: result.Key, view: view, oldPayload: oldPayload, blocks: blocks, writer: writer}
}

type plannerRootCreator struct {
	t       *testing.T
	scheme  commitment.IndexCommitment
	store   *materializermemory.Store
	objects []mutation.UpdateObject
}

func (c *plannerRootCreator) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	objectID := fmt.Sprintf("directory-%03d", len(c.objects)+1)
	entries := make([]arcset.ArcEntry, 0, len(bindings))
	values := make(map[arcset.Path]cid.Cid, len(bindings))
	for name, raw := range bindings {
		key, err := cid.Parse(raw)
		if err != nil {
			return cid.Undef, err
		}
		coordinate, err := arcset.NewMapCoordinate(name)
		if err != nil {
			return cid.Undef, err
		}
		entries = append(entries, arcset.ArcEntry{Coordinate: coordinate, Target: plannerTarget(key)})
		values[arcset.CanonicalizePath(name)] = key
	}
	canonical, err := arcset.NewCanonicalArcSet(arcset.KindMap, entries)
	if err != nil {
		return cid.Undef, err
	}
	semantics, err := mappingradix.NewMap(c.scheme, c.store)
	if err != nil {
		return cid.Undef, err
	}
	root, err := semantics.Commit(ctx, "client-root/v1/"+objectID, mapping.NewViewFromPaths(values))
	if err != nil {
		return cid.Undef, err
	}
	c.objects = append(c.objects, mutation.UpdateObject{ObjectID: objectID, Root: root, Kind: arcset.KindMap, Entries: canonical})
	return root, nil
}

type plannerBlocks struct {
	values        map[string][]byte
	substitutePut cid.Cid
}

func (b *plannerBlocks) Get(_ context.Context, key cid.Cid) ([]byte, error) {
	value, ok := b.values[key.KeyString()]
	if !ok {
		return nil, fmt.Errorf("block %s not found", key)
	}
	return append([]byte(nil), value...), nil
}

func (b *plannerBlocks) Put(_ context.Context, body []byte) (cid.Cid, error) {
	key, err := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}.Sum(body)
	if err != nil {
		return cid.Undef, err
	}
	b.values[key.KeyString()] = append([]byte(nil), body...)
	return key, nil
}

func (b *plannerBlocks) PutWithCodec(_ context.Context, body []byte, codec uint64) (cid.Cid, error) {
	key, err := cid.Prefix{Version: 1, Codec: codec, MhType: mh.SHA2_256, MhLength: -1}.Sum(body)
	if err != nil {
		return cid.Undef, err
	}
	b.values[key.KeyString()] = append([]byte(nil), body...)
	if b.substitutePut.Defined() {
		return b.substitutePut, nil
	}
	return key, nil
}

func (b *plannerBlocks) putRaw(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	key, err := b.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func plannerOperations(root cid.Cid, payload cid.Cid) []journal.Operation {
	now := time.Unix(1, 0).UTC()
	intents := []journal.Intent{
		{OperationID: "op-mkdir", RetryID: "retry-mkdir", DatasetID: "dataset", Branch: "main", BaseRoot: root.String(), BaseRevision: 7, Kind: journal.KindMkdir, Path: "archive"},
		{OperationID: "op-rename", RetryID: "retry-rename", DatasetID: "dataset", Branch: "main", BaseRoot: root.String(), BaseRevision: 7, Kind: journal.KindRename, Path: "docs/old.txt", Destination: "archive/old.txt"},
		{OperationID: "op-write", RetryID: "retry-write", DatasetID: "dataset", Branch: "main", BaseRoot: root.String(), BaseRevision: 7, Kind: journal.KindWrite, Path: "docs/new.txt", PayloadCID: payload.String()},
		{OperationID: "op-unlink", RetryID: "retry-unlink", DatasetID: "dataset", Branch: "main", BaseRoot: root.String(), BaseRevision: 7, Kind: journal.KindUnlink, Path: "docs/delete.txt"},
	}
	operations := make([]journal.Operation, len(intents))
	for index, intent := range intents {
		operations[index] = journal.Operation{Intent: intent, Sequence: uint64(index + 1), Status: journal.StatusPendingUpload, CreatedAt: now, UpdatedAt: now}
	}
	return operations
}

func assertPlannedTree(t *testing.T, planner *Planner, layout unixfs.LayoutKind, view mutation.UpdateView) {
	t.Helper()
	top := objectByRoot(t, view, view.BaseRoot)
	objects := make(map[string]*mutation.UpdateObject, len(view.Objects))
	for index := range view.Objects {
		object := &view.Objects[index]
		objects[object.Root.KeyString()] = object
	}
	var root *treeNode
	var err error
	if layout == unixfs.LayoutFlatV1 {
		root, err = planner.loadFlat(t.Context(), top)
	} else {
		root, err = planner.loadHybrid(t.Context(), top, objects, map[string]bool{})
	}
	if err != nil {
		t.Fatal(err)
	}
	archive := root.children["archive"]
	docs := root.children["docs"]
	if archive == nil || archive.kind != unixfsmodel.DirectoryEntryTypeDir || archive.children["old.txt"] == nil {
		t.Fatalf("planned archive tree=%#v", archive)
	}
	if docs == nil || docs.kind != unixfsmodel.DirectoryEntryTypeDir || len(docs.children) != 1 || docs.children["new.txt"] == nil {
		t.Fatalf("planned docs tree=%#v", docs)
	}
}

func objectByRoot(t *testing.T, view mutation.UpdateView, root cid.Cid) *mutation.UpdateObject {
	t.Helper()
	for index := range view.Objects {
		if view.Objects[index].Root.Equals(root) {
			return &view.Objects[index]
		}
	}
	t.Fatalf("object %s not found", root)
	return nil
}

func plannerTarget(key cid.Cid) arcset.TargetRef {
	switch maltcid.SemanticKindOf(key) {
	case maltcid.SemanticKindMap:
		return arcset.NewMapTarget(key)
	case maltcid.SemanticKindList:
		return arcset.NewListTarget(key)
	default:
		return arcset.NewCASTarget(key)
	}
}

func plannerRawCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	key, err := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}.Sum(body)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func mustKZG(t *testing.T) commitment.IndexCommitment {
	t.Helper()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	return scheme
}
