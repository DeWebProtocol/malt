package rq2write_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/evaluation/authenticationgraph"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2e0"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2write"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/engine"
	cid "github.com/ipfs/go-cid"
)

func TestCurrentProjectionAllFileOperationsAndSharedMeasuredChildren(t *testing.T) {
	ctx := context.Background()
	seed := sha256.Sum256([]byte("typed RQ2 fixture"))
	zero := uint64(0)
	source := &rq2fixture.SourceDefinition{SchemaVersion: rq2fixture.SourceSchemaVersion, FixtureID: "typed-projection", MutationSeedSHA256: hex.EncodeToString(seed[:]), DirectFiles: []rq2fixture.SourceDirectFile{}, Operations: []rq2fixture.Operation{
		{Name: "append", Kind: rq2fixture.KindListAppend, SourcePath: "large.bin", SourceCoordinate: "large.bin", PayloadBytes: 4},
		{Name: "replace-large-file-chunk", Kind: rq2fixture.KindListReplace, SourcePath: "large.bin", SourceCoordinate: "large.bin", PayloadBytes: 4, ListIndex: &zero},
		{Name: "batch-sync", Kind: rq2fixture.KindBatchInsert, Batch: []rq2fixture.BatchTarget{{Path: "batch/one", Coordinate: "batch/one", PayloadBytes: 4}, {Path: "batch/two", Coordinate: "batch/two", PayloadBytes: 4}}},
		{Name: "create-small-file", Kind: rq2fixture.KindDirectInsert, DestinationPath: "created", DestinationCoordinate: "created", PayloadBytes: 4},
		{Name: "insert-directory-entry", Kind: rq2fixture.KindDirectInsert, DestinationPath: "inserted", DestinationCoordinate: "inserted", PayloadBytes: 4},
		{Name: "modify-small-file", Kind: rq2fixture.KindDirectReplace, SourcePath: "modify", SourceCoordinate: "modify", PayloadBytes: 4},
		{Name: "delete-directory-entry", Kind: rq2fixture.KindDirectDelete, SourcePath: "delete", SourceCoordinate: "delete"},
		{Name: "rename", Kind: rq2fixture.KindDirectMove, SourcePath: "rename", SourceCoordinate: "rename", DestinationPath: "renamed", DestinationCoordinate: "renamed"},
		{Name: "move", Kind: rq2fixture.KindDirectMove, SourcePath: "move", SourceCoordinate: "move", DestinationPath: "directory/moved", DestinationCoordinate: "directory/moved"},
		{Name: "document-edit-cid-binding-submit", Kind: rq2fixture.KindDocumentEdit, SourcePath: "document", SourceCoordinate: "document", PayloadBytes: 4},
	}}
	for _, path := range []string{"modify", "delete", "rename", "move", "document"} {
		source.DirectFiles = append(source.DirectFiles, rq2fixture.SourceDirectFile{Path: path, Coordinate: path, Bytes: []byte("old " + path)})
	}
	for _, path := range []string{"large.bin", "alias.bin"} {
		source.ListFiles = append(source.ListFiles, rq2fixture.SourceListFile{Path: path, Coordinate: path, ChunkSize: 4, TotalSize: 8, Chunks: []rq2fixture.SourceListChunk{{Index: 0, Bytes: []byte("one!")}, {Index: 1, Bytes: []byte("two!")}}})
	}
	fixture, err := rq2e0.BuildFixture(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"kzg", "ipa"} {
		t.Run(backend, func(t *testing.T) {
			gateway, root, err := rq2e0.NewConformanceGateway(fixture, backend, strings.Repeat("a", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer gateway.Close()
			remote, err := gatewaytransport.New(gatewaytransport.Options{BaseURL: gateway.URL(), InstanceToken: strings.Repeat("a", 64)})
			if err != nil {
				t.Fatal(err)
			}
			payloads, err := transport.New(transport.Options{BaseURL: gateway.URL(), HTTPClient: remote.InstanceHTTPClient()})
			if err != nil {
				t.Fatal(err)
			}
			var scheme engine.Profile
			if backend == "kzg" {
				scheme, err = kzg.NewScheme()
			} else {
				scheme, err = ipa.NewScheme()
			}
			if err != nil {
				t.Fatal(err)
			}
			registry := engine.NewRegistry()
			if err := registry.Register(scheme); err != nil {
				t.Fatal(err)
			}
			session, err := authenticationgraph.New(remote, engine.New(registry))
			if err != nil {
				t.Fatal(err)
			}
			load, err := session.Load(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			if load.Objects != 2 {
				t.Fatal("shared child imported twice")
			}
			initial, err := session.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := fixture.ValidateInitialGraph(initial, backend); err != nil {
				t.Fatal(err)
			}
			currentSource := fixture.InitialSource()
			for i, operation := range source.Operations {
				nextSource, bodies, err := fixture.ApplySourceOperation(currentSource, operation, uint64(i))
				if err != nil {
					t.Fatal(err)
				}
				targets := make([]cid.Cid, len(bodies))
				if len(bodies) > 0 {
					blocks := make([]transport.Block, len(bodies))
					for j, body := range bodies {
						blocks[j] = transport.Block{Codec: cid.Raw, Data: body}
					}
					upload, err := payloads.PutBatchMeasured(ctx, blocks)
					if err != nil {
						t.Fatal(err)
					}
					for j, result := range upload.Results {
						targets[j] = result.CID
					}
				}
				edit, err := session.Begin()
				if err != nil {
					t.Fatal(err)
				}
				next, err := rq2write.Apply(ctx, edit, root, operation, targets)
				if err != nil {
					t.Fatalf("%s: %v", operation.Name, err)
				}
				preview, err := edit.Preview(ctx, next)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.ValidateGraphAgainstSource(preview, backend, nextSource); err != nil {
					t.Fatalf("%s full oracle: %v", operation.Name, err)
				}
				result, err := session.Submit(ctx, fmt.Sprintf("operation-%d", i), edit, next)
				if err != nil {
					t.Fatal(err)
				}
				if result.Idempotent || result.Submission.WriteAccounting.Available {
					t.Fatal("correctness oracle claimed production accounting or returned replay")
				}
				currentSource = nextSource
				root = next
			}
			if err := session.Audit(ctx); err != nil {
				t.Fatal(err)
			}
			if got := gateway.Operations(); got != uint64(len(source.Operations)) {
				t.Fatalf("operations = %d", got)
			}
		})
	}
}
