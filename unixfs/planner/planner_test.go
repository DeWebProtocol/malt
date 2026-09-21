package planner

import (
	"context"
	"fmt"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	authbuiltin "github.com/dewebprotocol/malt-core/sdk/authentication/builtin"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	materializermemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type plannerBlocks struct {
	values            map[string][]byte
	substitutePut     cid.Cid
	putWithCodecCalls int
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
	b.putWithCodecCalls++
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

func plannerOperation(root cid.Cid, sequence uint64, kind journal.Kind, operationPath, destination string, payload cid.Cid) journal.Operation {
	now := time.Unix(1, 0).UTC()
	payloadText := ""
	if payload.Defined() {
		payloadText = payload.String()
	}
	return journal.Operation{
		Intent: journal.Intent{
			OperationID: fmt.Sprintf("op-%d", sequence), RetryID: fmt.Sprintf("retry-%d", sequence),
			DatasetID: "dataset", Branch: "main", BaseRoot: root.String(), BaseRevision: 7,
			Kind: kind, Path: operationPath, Destination: destination, PayloadCID: payloadText,
		},
		Sequence: sequence, Status: journal.StatusPendingUpload, CreatedAt: now, UpdatedAt: now,
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

func mustKZG(t *testing.T) commitment.Backend {
	t.Helper()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	return scheme
}

type plannerFixture struct {
	root       cid.Cid
	oldPayload cid.Cid
	layout     unixfs.LayoutKind
	blocks     *plannerBlocks
	engine     *engine.Engine
	nodes      *materializermemory.Nodes
	candidates map[string]protocol.AuthenticationCandidate
}

func newPlannerEnvironment(t *testing.T, layout unixfs.LayoutKind, scheme commitment.Backend) (*plannerFixture, *unixfs.AuthenticationAdapter) {
	t.Helper()
	profile := scheme.(engine.Profile)
	profiles := engine.NewRegistry()
	if err := profiles.Register(profile); err != nil {
		t.Fatal(err)
	}
	f := &plannerFixture{layout: layout, blocks: &plannerBlocks{values: map[string][]byte{}}, engine: engine.New(input.DefaultRegistry(), profiles), nodes: materializermemory.NewNodes(), candidates: map[string]protocol.AuthenticationCandidate{}}
	creator, err := unixfs.NewAuthenticationAdapter(layout, f, f.engine, profile.ProfileID())
	if err != nil {
		t.Fatal(err)
	}
	return f, creator
}

func newPlannerFixture(t *testing.T, layoutKind unixfs.LayoutKind, scheme commitment.Backend) *plannerFixture {
	t.Helper()
	f, creator := newPlannerEnvironment(t, layoutKind, scheme)
	f.oldPayload = f.blocks.putRaw(t, []byte("old body"))
	deletePayload := f.blocks.putRaw(t, []byte("delete body"))
	root := unixfs.NewStagedDirectory()
	if err := unixfs.SetStagedFile(root, "docs/old.txt", f.oldPayload); err != nil {
		t.Fatal(err)
	}
	if err := unixfs.SetStagedFile(root, "docs/delete.txt", deletePayload); err != nil {
		t.Fatal(err)
	}
	layout, err := unixfs.NewLayout(layoutKind)
	if err != nil {
		t.Fatal(err)
	}
	result, err := layout.Materialize(t.Context(), creator, f.blocks, root)
	if err != nil {
		t.Fatal(err)
	}
	f.root = result.Key
	f.blocks.putWithCodecCalls = 0
	return f
}

func newSharedHybridFixture(t *testing.T, scheme commitment.Backend) *plannerFixture {
	t.Helper()
	f, creator := newPlannerEnvironment(t, unixfs.LayoutHybridV1, scheme)
	empty, err := unixfsmodel.EncodeDirectoryManifest(nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyManifest, err := f.blocks.PutWithCodec(t.Context(), empty.Data, empty.Codec)
	if err != nil {
		t.Fatal(err)
	}
	child, err := creator.UpdateStagedRoot(t.Context(), cid.Undef, map[string]string{"@payload": emptyManifest.String()})
	if err != nil {
		t.Fatal(err)
	}
	top, err := unixfsmodel.EncodeDirectoryManifest([]unixfsmodel.DirectoryEntry{{Name: "alpha", Type: unixfsmodel.DirectoryEntryTypeDir}, {Name: "beta", Type: unixfsmodel.DirectoryEntryTypeDir}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := f.blocks.PutWithCodec(t.Context(), top.Data, top.Codec)
	if err != nil {
		t.Fatal(err)
	}
	f.root, err = creator.UpdateStagedRoot(t.Context(), cid.Undef, map[string]string{"@payload": manifest.String(), "alpha": child.String(), "beta": child.String()})
	if err != nil {
		t.Fatal(err)
	}
	f.blocks.putWithCodecCalls = 0
	return f
}

func (f *plannerFixture) AuthenticationCandidate(_ context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	value, ok := f.candidates[root.KeyString()]
	if !ok {
		return nil, fmt.Errorf("candidate %s missing", root)
	}
	return &value, nil
}
func (f *plannerFixture) MaterializeAuthentication(ctx context.Context, c protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := authentication.Materialize(ctx, f.engine, c, f.nodes); err != nil {
		return cid.Undef, err
	}
	root := cid.MustParse(c.Root)
	f.candidates[root.KeyString()] = c
	return root, nil
}
func (f *plannerFixture) planner(t *testing.T) *Planner {
	t.Helper()
	p, err := New(f.layout, f.blocks, f, f.engine)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f *plannerFixture) install(t *testing.T, candidates []protocol.AuthenticationCandidate, root cid.Cid) {
	t.Helper()
	batch := protocol.AuthenticationBatch{Profile: protocol.AuthenticationBatchProfile, TransactionID: "test", Base: f.root.String(), Root: root.String(), Candidates: candidates}
	if err := authentication.ValidateBatch(t.Context(), f.engine, batch); err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if _, err := f.MaterializeAuthentication(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *plannerFixture) resolve(t *testing.T, root cid.Cid, path string) (cid.Cid, bool) {
	t.Helper()
	labels := []string{path}
	if f.layout == unixfs.LayoutRootedV1 {
		labels = strings.Split(path, "/")
	}
	steps := make([]input.Value, len(labels))
	for i, label := range labels {
		steps[i] = input.LabelValue([]byte(label))
	}
	query := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Operation: "resolve", Steps: steps}
	result, err := authentication.Execute(t.Context(), f.engine, query, f.nodes)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := authbuiltin.NewVerifier(nil)
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := authentication.Verify(verifier, query, result); err != nil || !valid {
		t.Fatal("independent path verification failed", err)
	}
	if result.AbsentStep != nil {
		return cid.Undef, false
	}
	return cid.MustParse(result.Resolved), true
}

func containsPayload(values []cid.Cid, target cid.Cid) bool {
	for _, value := range values {
		if value.Equals(target) {
			return true
		}
	}
	return false
}

func TestPlannerComputesVerifiableCandidatesAcrossLayoutsAndBackends(t *testing.T) {
	for _, backend := range []string{"kzg", "ipa"} {
		for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
			t.Run(backend+"/"+string(layout), func(t *testing.T) {
				f := newPlannerFixture(t, layout, plannerScheme(t, backend))
				payload := f.blocks.putRaw(t, []byte("new body"))
				candidates, required, root, err := f.planner(t).Plan(t.Context(), f.root, plannerOperations(f.root, payload))
				if err != nil {
					t.Fatal(err)
				}
				if root.Equals(f.root) || !containsPayload(required, payload) {
					t.Fatal("changed final payload omitted")
				}
				f.install(t, candidates, root)
				for name, expected := range map[string]cid.Cid{"archive/old.txt": f.oldPayload, "docs/new.txt": payload} {
					got, found := f.resolve(t, root, name)
					if !found || !got.Equals(expected) {
						t.Fatalf("%s: %s", name, got)
					}
				}
				for _, name := range []string{"docs/old.txt", "docs/delete.txt"} {
					if _, found := f.resolve(t, root, name); found {
						t.Fatal("removed path still present", name)
					}
				}
				if old, found := f.resolve(t, f.root, "docs/old.txt"); !found || !old.Equals(f.oldPayload) {
					t.Fatal("old root changed")
				}
			})
		}
	}
}
func TestPlannerRejectsManifestCIDSubstitutionAndCorruptOldManifest(t *testing.T) {
	f := newPlannerFixture(t, unixfs.LayoutHybridV1, mustKZG(t))
	p := f.planner(t)
	operations := plannerOperations(f.root, f.blocks.putRaw(t, []byte("new")))[:1]
	f.blocks.substitutePut = plannerRawCID(t, []byte("substituted manifest"))
	if _, _, _, err := p.Plan(t.Context(), f.root, operations); err == nil {
		t.Fatal("manifest CID substitution accepted")
	}
	f.blocks.substitutePut = cid.Undef
	for _, entry := range f.candidates[f.root.KeyString()].State.Entries {
		if entry.Input.Kind == input.System {
			f.blocks.values[entry.Target.KeyString()] = []byte("corrupt manifest")
		}
	}
	if _, _, _, err := p.Plan(t.Context(), f.root, operations); err == nil {
		t.Fatal("corrupt old manifest accepted")
	}
}
func TestPlannerRejectsPartialViewIdentityAndUnfrozenOperations(t *testing.T) {
	f := newPlannerFixture(t, unixfs.LayoutFlatV1, mustKZG(t))
	p := f.planner(t)
	payload := f.blocks.putRaw(t, []byte("new"))
	operations := plannerOperations(f.root, payload)
	operations[1].BaseRevision++
	if _, _, _, err := p.Plan(t.Context(), f.root, operations); err == nil {
		t.Fatal("cross-View operation batch accepted")
	}
	operations = plannerOperations(f.root, payload)
	operations[0].Status = journal.StatusLocalDirty
	if _, _, _, err := p.Plan(t.Context(), f.root, operations); err == nil {
		t.Fatal("unfrozen operation accepted")
	}
	operations = plannerOperations(f.root, payload)
	candidate := f.candidates[f.root.KeyString()]
	candidate.State.Entries = candidate.State.Entries[:1]
	f.candidates[f.root.KeyString()] = candidate
	if _, _, _, err := p.Plan(t.Context(), f.root, operations); err == nil {
		t.Fatal("incomplete authenticated state accepted")
	}
}
func TestPlannerClassifiesEquivalentContentAndCanceledNamespaceAsNoChange(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
		t.Run(string(layout), func(t *testing.T) {
			f := newPlannerFixture(t, layout, mustKZG(t))
			p := f.planner(t)
			same := []journal.Operation{plannerOperation(f.root, 1, journal.KindWrite, "docs/old.txt", "", f.oldPayload)}
			cancel := []journal.Operation{plannerOperation(f.root, 1, journal.KindMkdir, "temporary", "", cid.Undef), plannerOperation(f.root, 2, journal.KindUnlink, "temporary", "", cid.Undef)}
			for _, ops := range [][]journal.Operation{same, cancel} {
				candidates, _, root, err := p.Plan(t.Context(), f.root, ops)
				if err != nil || !root.Equals(f.root) || len(candidates) != 0 {
					t.Fatal("no-change batch produced output", root, err)
				}
				if f.blocks.putWithCodecCalls != 0 {
					t.Fatal("no-change batch published manifests")
				}
			}
		})
	}
}
func TestHybridPlannerCopyOnWritesSharedDirectoriesAcrossBackends(t *testing.T) {
	for _, backend := range []string{"kzg", "ipa"} {
		for _, equal := range []bool{false, true} {
			t.Run(fmt.Sprint(backend, "/equal=", equal), func(t *testing.T) {
				f := newSharedHybridFixture(t, plannerScheme(t, backend))
				first := f.blocks.putRaw(t, []byte("first alias"))
				second := f.blocks.putRaw(t, []byte("second alias"))
				if equal {
					second = first
				}
				ops := []journal.Operation{plannerOperation(f.root, 1, journal.KindWrite, "alpha/file", "", first), plannerOperation(f.root, 2, journal.KindWrite, "beta/file", "", second)}
				candidates, _, root, err := f.planner(t).Plan(t.Context(), f.root, ops)
				if err != nil {
					t.Fatal(err)
				}
				f.install(t, candidates, root)
				alpha, _ := f.resolve(t, root, "alpha")
				beta, _ := f.resolve(t, root, "beta")
				if alpha.Equals(beta) != equal {
					t.Fatal("shared directory isolation/reuse differs")
				}
				want := 3
				if equal {
					want = 2
				}
				if len(candidates) != want {
					t.Fatal("duplicate shared output", len(candidates))
				}
				for name, want := range map[string]cid.Cid{"alpha/file": first, "beta/file": second} {
					got, found := f.resolve(t, root, name)
					if !found || !got.Equals(want) {
						t.Fatal("shared child content changed", name)
					}
				}
			})
		}
	}
}
func TestPlannerSelectsOnlyFinalStagedPayloads(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
		t.Run(string(layout), func(t *testing.T) {
			f := newPlannerFixture(t, layout, mustKZG(t))
			p := f.planner(t)
			secret := f.blocks.putRaw(t, []byte("transient secret"))
			public := f.blocks.putRaw(t, []byte("final public body"))
			ops := []journal.Operation{plannerOperation(f.root, 1, journal.KindWrite, "docs/new.txt", "", secret), plannerOperation(f.root, 2, journal.KindWrite, "docs/new.txt", "", public), plannerOperation(f.root, 3, journal.KindMkdir, "archive", "", cid.Undef)}
			_, required, root, err := p.Plan(t.Context(), f.root, ops)
			if err != nil || root.Equals(f.root) || !containsPayload(required, public) || containsPayload(required, secret) {
				t.Fatal("overwritten payload survived final projection", err)
			}
			ops = []journal.Operation{plannerOperation(f.root, 1, journal.KindWrite, "docs/transient.txt", "", secret), plannerOperation(f.root, 2, journal.KindUnlink, "docs/transient.txt", "", cid.Undef)}
			candidates, required, root, err := p.Plan(t.Context(), f.root, ops)
			if err != nil || !root.Equals(f.root) || len(candidates) != 0 || containsPayload(required, secret) {
				t.Fatal("deleted payload survived projection", err)
			}
		})
	}
}
func plannerScheme(t *testing.T, backend string) commitment.Backend {
	t.Helper()
	if backend == "kzg" {
		return mustKZG(t)
	}
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileDirect)
	if err != nil {
		t.Fatal(err)
	}
	return scheme
}
