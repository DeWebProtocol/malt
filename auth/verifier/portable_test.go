package verifier_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	listtree "github.com/dewebprotocol/malt/runtime/semantic/list/tree"
	mapradix "github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
	kvmemory "github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestPortableVerifierAcceptsRuntimeRadixAndTreeProofs(t *testing.T) {
	factories := map[string]func(*testing.T) commitment.IndexCommitment{
		"ipa": func(t *testing.T) commitment.IndexCommitment {
			t.Helper()
			scheme, err := ipa.NewScheme()
			if err != nil {
				t.Fatalf("ipa.NewScheme: %v", err)
			}
			return scheme
		},
		"kzg": func(t *testing.T) commitment.IndexCommitment {
			t.Helper()
			scheme, err := kzg.NewScheme()
			if err != nil {
				t.Fatalf("kzg.NewScheme: %v", err)
			}
			return scheme
		},
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			table, err := overwrite.NewArcTable(overwrite.WithKVStore(kvmemory.New()))
			if err != nil {
				t.Fatalf("NewArcTable: %v", err)
			}
			scheme := factory(t)
			maps, err := mapradix.NewMap(scheme, table)
			if err != nil {
				t.Fatalf("radix.NewMap: %v", err)
			}
			lists, err := listtree.NewList(scheme, table)
			if err != nil {
				t.Fatalf("tree.NewList: %v", err)
			}
			portable, err := authverifier.NewDefault()
			if err != nil {
				t.Fatalf("NewDefault: %v", err)
			}

			t.Run("map", func(t *testing.T) {
				target := portableTestCID(t, "profile-name")
				root, err := maps.Commit(ctx, "portable-map-"+name, mapping.NewViewFrom(map[string]cid.Cid{"profile/name": target}))
				if err != nil {
					t.Fatalf("Commit: %v", err)
				}
				binding, proof, err := maps.Prove(ctx, "portable-map-"+name, root, arcset.CanonicalizePath("profile/name"))
				if err != nil {
					t.Fatalf("Prove: %v", err)
				}
				pl := prooflist.ProofList{Root: root, Query: "profile/name", Steps: []prooflist.Step{{
					Kind: prooflist.KindMapStep, From: root, Path: "profile/name", Target: binding.Value,
					EvidenceKind: "structure", EvidenceBackend: "map", Proof: proof,
				}}}
				assertPortableValid(t, portable, pl)

				pl.Steps[0].Target = portableTestCID(t, "forged-map-target")
				assertPortableRejected(t, portable, pl)
			})

			t.Run("list_index", func(t *testing.T) {
				values := []cid.Cid{portableTestCID(t, "list-0"), portableTestCID(t, "list-1")}
				root, err := lists.Commit(ctx, "portable-list-"+name, list.NewViewFromSlice(values))
				if err != nil {
					t.Fatalf("Commit: %v", err)
				}
				index := uint64(1)
				query, proof, err := lists.Prove(ctx, "portable-list-"+name, root, index)
				if err != nil {
					t.Fatalf("Prove: %v", err)
				}
				length := query.Length
				pl := prooflist.ProofList{Root: root, Query: "list:1", Steps: []prooflist.Step{{
					Kind: prooflist.KindListIndex, From: root, Index: &index, Length: &length, Target: query.Key,
					EvidenceKind: "structure", EvidenceBackend: "list", Proof: proof,
				}}}
				assertPortableValid(t, portable, pl)

				pl.Query = "list:0"
				assertPortableRejected(t, portable, pl)
			})

			t.Run("list_range", func(t *testing.T) {
				chunks := []cid.Cid{portableTestCID(t, "chunk-0"), portableTestCID(t, "chunk-1")}
				root, err := lists.CommitFixed(ctx, "portable-range-"+name, chunks, 8, 12)
				if err != nil {
					t.Fatalf("CommitFixed: %v", err)
				}
				start, end := uint64(2), uint64(10)
				result, proof, err := lists.ProveRange(ctx, "portable-range-"+name, root, start, &end)
				if err != nil {
					t.Fatalf("ProveRange: %v", err)
				}
				childCount := result.Metadata.ChildCount
				totalSize := result.Metadata.TotalSize
				chunkSize := result.Metadata.ChunkSize
				pl := prooflist.ProofList{Root: root, Query: "range:2:10", Steps: []prooflist.Step{{
					Kind: prooflist.KindListRange, From: root, Target: root, Start: &start, End: &end,
					ChildCount: &childCount, TotalSize: &totalSize, ChunkSize: &chunkSize, Segments: result.Segments,
					EvidenceKind: "structure", EvidenceBackend: "measured_list", Proof: proof,
				}}}
				assertPortableValid(t, portable, pl)

				pl.Query = "range:3:10"
				assertPortableRejected(t, portable, pl)
			})
		})
	}
}

func assertPortableValid(t *testing.T, verifier *authverifier.Verifier, pl prooflist.ProofList) {
	t.Helper()
	ok, err := verifier.VerifyProofList(context.Background(), pl)
	if err != nil {
		t.Fatalf("VerifyProofList: %v", err)
	}
	if !ok {
		t.Fatal("VerifyProofList returned false")
	}
}

func assertPortableRejected(t *testing.T, verifier *authverifier.Verifier, pl prooflist.ProofList) {
	t.Helper()
	ok, err := verifier.VerifyProofList(context.Background(), pl)
	if err == nil && ok {
		t.Fatal("VerifyProofList accepted tampered artifact")
	}
}

func portableTestCID(t *testing.T, seed string) cid.Cid {
	t.Helper()
	sum, err := mh.Sum([]byte(fmt.Sprintf("portable:%s", seed)), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("hash seed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}
