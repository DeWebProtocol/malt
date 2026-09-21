package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2e0"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2wire"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2write"
	"github.com/dewebprotocol/malt-client/transport"
)

func TestNativePlannerExecutesAllRegisteredOperationsWithKZGAndIPA(t *testing.T) {
	raw, err := os.ReadFile(writeNativeE0Fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := rq2fixture.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range []string{"kzg", "ipa"} {
		t.Run(backend, func(t *testing.T) {
			gateway, root, err := rq2e0.NewConformanceGateway(fixture, backend, strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer gateway.Close()
			evaluation, err := gatewaytransport.New(gatewaytransport.Options{BaseURL: gateway.URL(), InstanceToken: strings.Repeat("b", 64)})
			if err != nil {
				t.Fatal(err)
			}
			remote, err := transport.New(transport.Options{BaseURL: gateway.URL(), HTTPClient: evaluation.InstanceHTTPClient()})
			if err != nil {
				t.Fatal(err)
			}
			native, err := newNativeSession(workerConfig{clientKind: rq2wire.ClientNative, lifecycle: rq2wire.LifecycleNativeLong, backend: backend, requestTimeout: 30 * time.Second}, remote, evaluation, fixture)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := native.app.Load(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			workspace, err := newNativeWorkspace(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.close()
			for i, name := range []string{"append", "batch-sync", "create-small-file", "delete-directory-entry", "insert-directory-entry", "modify-small-file", "move", "rename", "replace-large-file-chunk"} {
				operation, err := fixture.Operation(name)
				if err != nil {
					t.Fatal(err)
				}
				prepared, err := prepareNativeOperation(operation, fixture, workspace, uint64(i))
				if err != nil {
					t.Fatal(err)
				}
				metadata := name == "delete-directory-entry" || name == "move" || name == "rename"
				if metadata {
					if prepared.scan.Applicable || prepared.chunk.Applicable || prepared.hash.Applicable || len(prepared.blocks) > 0 {
						t.Fatalf("metadata-only operation %s claimed payload work", name)
					}
				} else {
					if !prepared.scan.Applicable || !prepared.chunk.Applicable || !prepared.hash.Applicable || prepared.scan.Bytes == 0 || prepared.chunk.Count == 0 || prepared.hash.Count == 0 {
						t.Fatalf("payload phases missing for %s", name)
					}
					uploaded, err := remote.PutBatchMeasured(t.Context(), prepared.blocks)
					if err != nil {
						t.Fatal(err)
					}
					if len(uploaded.Results) != len(prepared.cids) {
						t.Fatal("payload result count differs")
					}
					for j, result := range uploaded.Results {
						if !result.CID.Equals(prepared.cids[j]) {
							t.Fatal("payload digest differs")
						}
					}
				}
				edit, err := native.app.Begin()
				if err != nil {
					t.Fatal(err)
				}
				next, err := rq2write.Apply(t.Context(), edit, root, operation, prepared.cids)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				view, err := edit.Preview(t.Context(), next)
				if err != nil {
					t.Fatal(err)
				}
				source, err := workspace.snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.ValidateGraphAgainstSource(view, backend, source); err != nil {
					t.Fatalf("%s full filesystem post-image: %v", name, err)
				}
				result, err := native.app.Submit(t.Context(), fmt.Sprintf("native-%d", i), edit, next)
				if err != nil {
					t.Fatal(err)
				}
				root = result.Root
			}
			if err := native.app.Audit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(workspace.root); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestBrowserPreflightFailsClosedWithoutRealBrowserWorker(t *testing.T) {
	w := &worker{config: workerConfig{clientKind: rq2wire.ClientBrowserWASM, backend: "kzg", lifecycle: rq2wire.LifecycleBrowserCold}}
	request := rq2wire.WorkerRequest{SchemaVersion: rq2wire.WorkerRequestSchema, WorkerID: "worker", RequestID: "preflight", RecordKind: rq2wire.RecordPreflight, SessionID: "session", ClientKind: rq2wire.ClientBrowserWASM, PlatformID: "browser", Backend: "kzg", Lifecycle: rq2wire.LifecycleBrowserCold, FixtureID: "fixture"}
	record := w.preflight(request)
	if record.Success || record.FailureClass != "capability_unavailable" {
		t.Fatalf("native worker claimed browser execution: %+v", record)
	}
}
