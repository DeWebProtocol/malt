package malt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	structure "github.com/dewebprotocol/malt/auth/semantic"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/graph/writer"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestEngineReadMapAndVerifyBindsRequest(t *testing.T) {
	root := testCID(t, "root")
	target := testCID(t, "target")
	key, err := arcset.NewPath("profile/name")
	if err != nil {
		t.Fatal(err)
	}
	maps := &fakeMaps{root: root, key: key, target: target, proof: []byte("map-proof")}
	engine, err := malt.NewEngine(malt.EngineOptions{
		Scope:    "physical-scope",
		Maps:     maps,
		Verifier: acceptingVerifier{},
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := malt.MapKeyQuery("profile/name")
	if err != nil {
		t.Fatal(err)
	}
	req := malt.ReadRequest{Root: root, Query: query}
	result, err := engine.Read(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Target.Equals(target) {
		t.Fatalf("target = %s, want %s", result.Target, target)
	}
	if maps.scope != "physical-scope" {
		t.Fatalf("map scope = %q", maps.scope)
	}
	if err := engine.VerifyRead(context.Background(), req, result); err != nil {
		t.Fatalf("VerifyRead: %v", err)
	}

	tampered := result
	tampered.Target = testCID(t, "tampered")
	if err := engine.VerifyRead(context.Background(), req, tampered); err == nil {
		t.Fatal("VerifyRead accepted a target not bound by ProofList")
	}
}

func TestEngineReadPayloadUsesTerminalProofKind(t *testing.T) {
	root := testCID(t, "payload-root")
	target := testCID(t, "payload-target")
	engine, err := malt.NewEngine(malt.EngineOptions{
		Scope: "payload-scope",
		Maps: &fakeMaps{
			root:   root,
			key:    arcset.PayloadPath,
			target: target,
			proof:  []byte("payload-proof"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := malt.MapKeyQuery(arcset.PayloadPath.String())
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Read(context.Background(), malt.ReadRequest{Root: root, Query: query})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.ProofList.Steps[0].Kind; got != prooflist.KindPayloadBinding {
		t.Fatalf("proof kind = %q, want %q", got, prooflist.KindPayloadBinding)
	}
}

func TestEngineReadListIndex(t *testing.T) {
	root := testCID(t, "list-root")
	target := testCID(t, "list-target")
	lists := fakeLists{root: root, target: target, length: 4, proof: []byte("list-proof")}
	engine, err := malt.NewEngine(malt.EngineOptions{
		Scope: "list-scope",
		Lists: lists,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Read(context.Background(), malt.ReadRequest{
		Root:  root,
		Query: malt.ListIndexQuery(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	step := result.ProofList.Steps[0]
	if step.Kind != prooflist.KindListIndex || step.Index == nil || *step.Index != 2 {
		t.Fatalf("list step = %+v", step)
	}
}

func TestEngineReadListRangeAndVerifyBindsSegments(t *testing.T) {
	root := testCID(t, "range-root")
	segments := []cid.Cid{testCID(t, "segment-a"), testCID(t, "segment-b")}
	lists := fakeMeasuredLists{
		root: root,
		result: list.RangeResult{
			Metadata: list.RangeMetadata{ChildCount: 2, TotalSize: 16, ChunkSize: 8},
			Segments: segments,
		},
		proof: []byte("range-proof"),
	}
	engine, err := malt.NewEngine(malt.EngineOptions{
		Scope:    "range-scope",
		Lists:    lists,
		Verifier: acceptingVerifier{},
	})
	if err != nil {
		t.Fatal(err)
	}
	end := uint64(10)
	query, err := malt.ListRangeQuery(2, &end)
	if err != nil {
		t.Fatal(err)
	}
	req := malt.ReadRequest{Root: root, Query: query}
	result, err := engine.Read(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 2 || !result.Segments[0].Equals(segments[0]) || !result.Segments[1].Equals(segments[1]) {
		t.Fatalf("segments = %v, want %v", result.Segments, segments)
	}
	if err := engine.VerifyRead(context.Background(), req, result); err != nil {
		t.Fatalf("VerifyRead: %v", err)
	}

	tampered := result
	tampered.Segments = []cid.Cid{segments[1]}
	if err := engine.VerifyRead(context.Background(), req, tampered); err == nil {
		t.Fatal("VerifyRead accepted segments not bound by ProofList")
	}
}

func TestEngineApplyHidesRuntimeScope(t *testing.T) {
	root := testCID(t, "base")
	newRoot := testCID(t, "next")
	applier := &fakeWriter{receipt: writer.WriteReceipt{BaseRoot: root, NewRoot: newRoot}}
	engine, err := malt.NewEngine(malt.EngineOptions{
		Scope:  "tenant-local-placement",
		Writer: applier,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := engine.Apply(context.Background(), writer.SemanticMutation{BaseRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if applier.scope != "tenant-local-placement" || !receipt.NewRoot.Equals(newRoot) {
		t.Fatalf("scope/receipt = %q/%s", applier.scope, receipt.NewRoot)
	}
}

func TestEngineReportsMissingReadCapability(t *testing.T) {
	engine, err := malt.NewEngine(malt.EngineOptions{
		Scope:  "write-only",
		Writer: &fakeWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := malt.MapKeyQuery("profile/name")
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Read(context.Background(), malt.ReadRequest{Root: testCID(t, "root"), Query: query})
	if !errors.Is(err, malt.ErrCapabilityUnavailable) {
		t.Fatalf("Read error = %v, want ErrCapabilityUnavailable", err)
	}
}

func TestVerifyReadRejectsCrossKindProof(t *testing.T) {
	root := testCID(t, "cross-kind-root")
	target := testCID(t, "cross-kind-target")
	index := uint64(1)
	length := uint64(2)
	req := malt.ReadRequest{Root: root, Query: malt.ListIndexQuery(index)}
	result := malt.ReadResult{
		Target: target,
		ProofList: prooflist.ProofList{
			Root:  root,
			Query: req.Query.String(),
			Steps: []prooflist.Step{{
				Kind:            prooflist.KindMapStep,
				From:            root,
				Path:            req.Query.String(),
				Index:           &index,
				Length:          &length,
				Target:          target,
				EvidenceKind:    "structure",
				EvidenceBackend: "map",
				Proof:           []byte("valid-map-proof"),
			}},
		},
	}
	if err := malt.VerifyRead(context.Background(), req, result, acceptingVerifier{}); err == nil {
		t.Fatal("VerifyRead accepted a map proof for a list-index query")
	}
}

type acceptingVerifier struct{}

func (acceptingVerifier) VerifyProofList(context.Context, prooflist.ProofList) (bool, error) {
	return true, nil
}

type fakeWriter struct {
	scope   string
	receipt writer.WriteReceipt
}

func (w *fakeWriter) Apply(_ context.Context, scope string, _ writer.SemanticMutation) (writer.WriteReceipt, error) {
	w.scope = scope
	return w.receipt, nil
}

type fakeMaps struct {
	root   cid.Cid
	key    arcset.Path
	target cid.Cid
	proof  structure.Proof
	scope  string
}

func (m *fakeMaps) Prove(_ context.Context, scope string, root cid.Cid, key arcset.Path) (mapping.Binding, structure.Proof, error) {
	m.scope = scope
	if !root.Equals(m.root) || key != m.key {
		return mapping.Binding{}, nil, arcset.ErrNotFound
	}
	return mapping.Binding{Value: m.target, Present: true}, append(structure.Proof(nil), m.proof...), nil
}

type fakeLists struct {
	root   cid.Cid
	target cid.Cid
	length uint64
	proof  structure.Proof
}

func (l fakeLists) Prove(_ context.Context, _ string, root cid.Cid, _ uint64) (list.Query, structure.Proof, error) {
	if !root.Equals(l.root) {
		return list.Query{}, nil, arcset.ErrNotFound
	}
	return list.Query{Key: l.target, Length: l.length}, append(structure.Proof(nil), l.proof...), nil
}

type fakeMeasuredLists struct {
	root   cid.Cid
	result list.RangeResult
	proof  structure.Proof
}

func (l fakeMeasuredLists) Prove(context.Context, string, cid.Cid, uint64) (list.Query, structure.Proof, error) {
	return list.Query{}, nil, errors.New("index proof not implemented")
}

func (l fakeMeasuredLists) ProveRange(_ context.Context, _ string, root cid.Cid, _ uint64, _ *uint64) (list.RangeResult, structure.Proof, error) {
	if !root.Equals(l.root) {
		return list.RangeResult{}, nil, arcset.ErrNotFound
	}
	return l.result, append(structure.Proof(nil), l.proof...), nil
}

func testCID(t *testing.T, seed string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

var (
	_ malt.Reader             = (*malt.Engine)(nil)
	_ malt.MapReader          = (*fakeMaps)(nil)
	_ malt.ListReader         = fakeLists{}
	_ malt.MeasuredListReader = fakeMeasuredLists{}
	_ malt.MutationApplier    = (*fakeWriter)(nil)
)
