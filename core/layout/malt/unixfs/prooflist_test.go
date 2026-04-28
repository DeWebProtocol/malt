package unixfs_test

import (
	"bytes"
	"testing"

	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/prooflist"
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

func TestProofListFromStepsClassifiesTerminalPayloadBinding(t *testing.T) {
	root := testCID(t, "root")
	fileRoot := testCID(t, "file")
	payload := testCID(t, "payload")
	steps := []unixfs.Step{
		{
			Root:   root,
			Path:   arcset.CanonicalizePath("dir"),
			Target: fileRoot,
			Proof:  []byte("dir proof"),
		},
		{
			Root:   fileRoot,
			Path:   arcset.CanonicalizePath("@payload"),
			Target: payload,
			Proof:  []byte("payload proof"),
		},
	}

	pl, err := unixfs.ProofListFromSteps(root, "dir/file.txt", steps)
	if err != nil {
		t.Fatalf("ProofListFromSteps failed: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("ValidateShape failed: %v", err)
	}
	if pl.Query != "dir/file.txt" {
		t.Fatalf("query = %q, want dir/file.txt", pl.Query)
	}
	if len(pl.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(pl.Steps))
	}
	if pl.Steps[0].Kind != prooflist.KindMapStep {
		t.Fatalf("step 0 kind = %q, want %q", pl.Steps[0].Kind, prooflist.KindMapStep)
	}
	if pl.Steps[1].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("step 1 kind = %q, want %q", pl.Steps[1].Kind, prooflist.KindPayloadBinding)
	}
	if pl.Steps[1].Path != "@payload" {
		t.Fatalf("step 1 path = %q, want @payload", pl.Steps[1].Path)
	}
	if pl.Steps[1].EvidenceKind != "structure" || pl.Steps[1].EvidenceBackend != "map" {
		t.Fatalf("step 1 evidence labels = %q/%q, want structure/map", pl.Steps[1].EvidenceKind, pl.Steps[1].EvidenceBackend)
	}
	if !bytes.Equal(pl.Steps[1].Proof, []byte("payload proof")) {
		t.Fatalf("terminal proof bytes were not preserved")
	}
}
