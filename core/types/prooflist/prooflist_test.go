package prooflist

import (
	"bytes"
	"encoding/json"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func testCID(t *testing.T, seed string) cid.Cid {
	t.Helper()
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum failed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func TestProofListJSONRoundTripUsesStableBase64Bytes(t *testing.T) {
	root := testCID(t, "root")
	target := testCID(t, "target")
	pl := ProofList{
		Root:  root,
		Query: "dir/file",
		Steps: []Step{
			{
				Kind:            KindMapStep,
				From:            root,
				Path:            "dir",
				Query:           "dir/file",
				Target:          target,
				EvidenceKind:    "explicit",
				EvidenceBackend: "kzg",
				Evidence:        []byte{1, 2, 3},
				Proof:           []byte{4},
			},
		},
	}

	data, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if !bytes.Contains(data, []byte(`"evidence":"AQID"`)) {
		t.Fatalf("evidence bytes were not base64 encoded in JSON: %s", data)
	}
	if !bytes.Contains(data, []byte(`"proof":"BA=="`)) {
		t.Fatalf("proof bytes were not base64 encoded in JSON: %s", data)
	}

	var got ProofList
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if !got.Root.Equals(root) {
		t.Fatalf("root = %v, want %v", got.Root, root)
	}
	if got.Query != "dir/file" {
		t.Fatalf("query = %q, want %q", got.Query, "dir/file")
	}
	if len(got.Steps) != 1 {
		t.Fatalf("steps len = %d, want 1", len(got.Steps))
	}
	step := got.Steps[0]
	if step.Kind != KindMapStep {
		t.Fatalf("kind = %q, want %q", step.Kind, KindMapStep)
	}
	if !step.From.Equals(root) || !step.Target.Equals(target) {
		t.Fatalf("from/target did not round trip")
	}
	if step.Path != "dir" || step.Query != "dir/file" {
		t.Fatalf("path/query did not round trip: %#v", step)
	}
	if !bytes.Equal(step.Evidence, []byte{1, 2, 3}) {
		t.Fatalf("evidence = %v, want [1 2 3]", step.Evidence)
	}
	if !bytes.Equal(step.Proof, []byte{4}) {
		t.Fatalf("proof = %v, want [4]", step.Proof)
	}
}

func TestValidateShapeRejectsUndefinedRootTargetAndUnknownKind(t *testing.T) {
	root := testCID(t, "root")
	target := testCID(t, "target")

	if err := (ProofList{Root: cid.Undef}).ValidateShape(); err == nil {
		t.Fatal("expected undefined root to be rejected")
	}

	if err := (ProofList{
		Root: root,
		Steps: []Step{{
			Kind:   KindMapStep,
			From:   root,
			Path:   "dir",
			Target: cid.Undef,
		}},
	}).ValidateShape(); err == nil {
		t.Fatal("expected undefined target to be rejected")
	}

	if err := (ProofList{
		Root: root,
		Steps: []Step{{
			Kind:   StepKind("not_a_kind"),
			From:   root,
			Path:   "dir",
			Target: target,
		}},
	}).ValidateShape(); err == nil {
		t.Fatal("expected unknown kind to be rejected")
	}

	if err := (ProofList{
		Root: root,
		Steps: []Step{{
			Kind:   KindLegacyUnknown,
			From:   root,
			Path:   "legacy",
			Target: target,
		}},
	}).ValidateShape(); err != nil {
		t.Fatalf("legacy/unknown compatibility kind should be accepted: %v", err)
	}

	if err := (ProofList{Root: root}).ValidateShape(RequireSteps()); err == nil {
		t.Fatal("expected RequireSteps to reject an empty proof list")
	}
}

func TestValidateShapeRejectsUnanchoredSteps(t *testing.T) {
	root := testCID(t, "root")
	detached := testCID(t, "detached")
	target := testCID(t, "target")

	if err := (ProofList{
		Root: root,
		Steps: []Step{{
			Kind:   KindMapStep,
			From:   detached,
			Path:   "name",
			Target: target,
		}},
	}).ValidateShape(RequireSteps()); err == nil {
		t.Fatal("expected unanchored first step to be rejected")
	}

	mid := testCID(t, "mid")
	chunk0 := testCID(t, "chunk0")
	chunk1 := testCID(t, "chunk1")
	index0 := uint64(0)
	index1 := uint64(1)
	length := uint64(2)
	if err := (ProofList{
		Root: root,
		Steps: []Step{
			{
				Kind:   KindMapStep,
				From:   root,
				Path:   "large.bin",
				Target: mid,
			},
			{
				Kind:   KindListIndex,
				From:   mid,
				Index:  &index0,
				Length: &length,
				Target: chunk0,
			},
			{
				Kind:   KindListIndex,
				From:   mid,
				Index:  &index1,
				Length: &length,
				Target: chunk1,
			},
		},
	}).ValidateShape(RequireSteps()); err != nil {
		t.Fatalf("expected sibling list-index steps anchored at a prior target to pass: %v", err)
	}
}

func TestValidateShapeRejectsNonLinearTraversalSteps(t *testing.T) {
	root := testCID(t, "root")
	first := testCID(t, "first")
	sibling := testCID(t, "sibling")

	if err := (ProofList{
		Root: root,
		Steps: []Step{
			{
				Kind:   KindMapStep,
				From:   root,
				Path:   "a",
				Target: first,
			},
			{
				Kind:   KindMapStep,
				From:   root,
				Path:   "b",
				Target: sibling,
			},
		},
	}).ValidateShape(RequireSteps()); err == nil {
		t.Fatal("expected a traversal step that branches from an earlier anchor to be rejected")
	}
}
