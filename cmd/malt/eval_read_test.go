package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/internal/eval/readbench"
	"github.com/dewebprotocol/malt/server"
	cid "github.com/ipfs/go-cid"
)

func TestEvalReadPrintsBenchmarkJSONL(t *testing.T) {
	ctx := context.Background()
	apiBaseURL, cfgPath, manifestCID, dummyCID := newEvalReadTestConfigWithCIDs(t)
	if apiBaseURL == "" {
		t.Fatal("test API base URL should not be empty")
	}

	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	resetEvalReadFlags(t)
	evalReadFixture = "eval-read-cli"
	evalReadDepth = 1
	evalReadSmallBytes = 40
	evalReadLargeBytes = 300 * 1024
	evalReadRange = "bytes=3-8"
	evalReadIterations = 1
	evalReadArcFlags = []string{
		"@payload=" + manifestCID.String(),
		"dummy=" + dummyCID.String(),
	}

	out := captureStdout(t, func() {
		if err := runEvalRead(testCommandWithContext(ctx), nil); err != nil {
			t.Fatalf("run eval-read: %v", err)
		}
	})

	results := decodeEvalReadJSONL(t, out)
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2\n%s", len(results), out)
	}
	if results[0].OperationKind != readbench.OperationProofListPath {
		t.Fatalf("first operation = %q", results[0].OperationKind)
	}
	if results[1].OperationKind != readbench.OperationContentRange {
		t.Fatalf("second operation = %q", results[1].OperationKind)
	}
	if results[1].ContentBytes == nil || *results[1].ContentBytes != 6 {
		t.Fatalf("content bytes = %v, want 6", results[1].ContentBytes)
	}
	if results[0].Proof.ProofListCount != 1 || results[1].Proof.ProofListCount != 1 {
		t.Fatalf("proof metrics should be reset per operation: %+v %+v", results[0].Proof, results[1].Proof)
	}
}

func TestEvalReadAcceptsArcFlags(t *testing.T) {
	ctx := context.Background()
	_, cfgPath, manifestCID, dummyCID := newEvalReadTestConfigWithCIDs(t)

	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	resetEvalReadFlags(t)
	evalReadFixture = "eval-read-cli-arc"
	evalReadDepth = 0
	evalReadSmallBytes = 40
	evalReadLargeBytes = 300 * 1024
	evalReadIterations = 1
	evalReadArcFlags = []string{
		"@payload=" + manifestCID.String(),
		"dummy=" + dummyCID.String(),
	}

	out := captureStdout(t, func() {
		if err := runEvalRead(testCommandWithContext(ctx), nil); err != nil {
			t.Fatalf("run eval-read with --arc flags: %v", err)
		}
	})
	results := decodeEvalReadJSONL(t, out)
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2\n%s", len(results), out)
	}
}

func TestEvalReadRequiresArcFlags(t *testing.T) {
	ctx := context.Background()
	_, cfgPath := newEvalReadTestConfig(t)

	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	resetEvalReadFlags(t)
	err := runEvalRead(testCommandWithContext(ctx), nil)
	if err == nil {
		t.Fatal("expected eval-read without --arc to fail")
	}
	if !strings.Contains(err.Error(), "--arc is required") {
		t.Fatalf("error = %q, want --arc is required", err.Error())
	}
}

func decodeEvalReadJSONL(t *testing.T, raw string) []readbench.Result {
	t.Helper()

	var results []readbench.Result
	scanner := bufio.NewScanner(bytes.NewBufferString(raw))
	for scanner.Scan() {
		var result readbench.Result
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			t.Fatalf("decode eval-read JSONL %q: %v", scanner.Text(), err)
		}
		results = append(results, result)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan eval-read output: %v", err)
	}
	return results
}

func resetEvalReadFlags(t *testing.T) {
	t.Helper()

	oldFixture := evalReadFixture
	oldDepth := evalReadDepth
	oldSmallBytes := evalReadSmallBytes
	oldLargeBytes := evalReadLargeBytes
	oldRange := evalReadRange
	oldIterations := evalReadIterations
	oldArcFlags := evalReadArcFlags
	oldArcs := evalReadArcs
	t.Cleanup(func() {
		evalReadFixture = oldFixture
		evalReadDepth = oldDepth
		evalReadSmallBytes = oldSmallBytes
		evalReadLargeBytes = oldLargeBytes
		evalReadRange = oldRange
		evalReadIterations = oldIterations
		evalReadArcFlags = oldArcFlags
		evalReadArcs = oldArcs
	})
	evalReadArcFlags = nil
	evalReadArcs = nil
}

func newEvalReadTestConfig(t *testing.T) (string, string) {
	t.Helper()

	apiBaseURL, cfgPath, _, _ := newEvalReadTestConfigWithCIDs(t)
	return apiBaseURL, cfgPath
}

func newEvalReadTestConfigWithCIDs(t *testing.T) (string, string, cid.Cid, cid.Cid) {
	t.Helper()

	mockCAS := casmock.NewCAS(casmock.WithoutLatency())

	ctx := context.Background()

	// Create a proper fixture root with a valid @payload in CAS.
	manifestData := []byte(`{"entries":["dummy"]}`)
	manifestCID, err := mockCAS.Put(ctx, manifestData)
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	dummyData := []byte("dummy")
	dummyCID, err := mockCAS.Put(ctx, dummyData)
	if err != nil {
		t.Fatalf("put dummy: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.RPC.Listen = ""
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"

	node, err := api.NewNode(api.WithConfig(cfg), api.WithCAS(mockCAS))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	t.Cleanup(ts.Close)

	cfg.RPC.Listen = ts.URL
	cfgPath := filepath.Join(t.TempDir(), "malt.json")
	if err := config.WriteToFile(cfgPath, cfg); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("stat test config: %v", err)
	}
	return ts.URL, cfgPath, manifestCID, dummyCID
}
