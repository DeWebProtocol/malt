package readbench

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/metrics"
	"github.com/dewebprotocol/malt/server"
)

func TestPrepareFixtureCreatesDeterministicMALTUnixFSPaths(t *testing.T) {
	ctx := context.Background()
	baseURL, mockCAS := newTestDaemonWithCAS(t)
	runner := NewRunner(baseURL)

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

	fixture, err := runner.PrepareFixture(ctx, FixtureConfig{
		FixtureName:    "readbench-fixture",
		DirectoryDepth: 3,
		SmallFileBytes: 32,
		LargeFileBytes: 300 * 1024,
		Arcs: map[string]string{
			"@payload": manifestCID.String(),
			"dummy":    dummyCID.String(),
		},
	})
	if err != nil {
		t.Fatalf("PrepareFixture() error = %v", err)
	}

	if fixture.FixtureName != "readbench-fixture" {
		t.Fatalf("fixture fixture = %q", fixture.FixtureName)
	}
	if fixture.SmallPath != "dir00/dir01/dir02/small.txt" {
		t.Fatalf("small path = %q", fixture.SmallPath)
	}
	if fixture.LargePath != "dir00/dir01/dir02/large.bin" {
		t.Fatalf("large path = %q", fixture.LargePath)
	}

	client := daemonclient.NewWithBaseURL(baseURL)
	smallStat, err := client.Stat(ctx, fixture.Root, fixture.SmallPath)
	if err != nil {
		t.Fatalf("stat small fixture: %v", err)
	}
	if smallStat.Kind != "file" || smallStat.StorageKind != "raw" {
		t.Fatalf("small stat = %+v, want raw file", smallStat)
	}

	largeStat, err := client.Stat(ctx, fixture.Root, fixture.LargePath)
	if err != nil {
		t.Fatalf("stat large fixture: %v", err)
	}
	if largeStat.Kind != "file" || largeStat.StorageKind != "list" {
		t.Fatalf("large stat = %+v, want list-backed file", largeStat)
	}
	if largeStat.Size == nil || *largeStat.Size != int64(300*1024) {
		t.Fatalf("large size = %v, want %d", largeStat.Size, 300*1024)
	}
}

func TestPrepareFixtureSupportsZeroDirectoryDepth(t *testing.T) {
	ctx := context.Background()
	baseURL, mockCAS := newTestDaemonWithCAS(t)
	runner := NewRunner(baseURL)

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

	fixture, err := runner.PrepareFixture(ctx, FixtureConfig{
		FixtureName:    "readbench-root",
		DirectoryDepth: 0,
		SmallFileBytes: 8,
		LargeFileBytes: 300 * 1024,
		Arcs: map[string]string{
			"@payload": manifestCID.String(),
			"dummy":    dummyCID.String(),
		},
	})
	if err != nil {
		t.Fatalf("PrepareFixture() error = %v", err)
	}
	if fixture.SmallPath != "small.txt" {
		t.Fatalf("small path = %q, want root-level small.txt", fixture.SmallPath)
	}
	if fixture.LargePath != "large.bin" {
		t.Fatalf("large path = %q, want root-level large.bin", fixture.LargePath)
	}
}

func TestPrepareFixtureGeneratesUniqueFixtureNameWhenOmitted(t *testing.T) {
	ctx := context.Background()
	baseURL, mockCAS := newTestDaemonWithCAS(t)
	runner := NewRunner(baseURL)

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

	cfg := FixtureConfig{
		DirectoryDepth: 1,
		SmallFileBytes: 8,
		LargeFileBytes: 300 * 1024,
		Arcs: map[string]string{
			"@payload": manifestCID.String(),
			"dummy":    dummyCID.String(),
		},
	}
	first, err := runner.PrepareFixture(ctx, cfg)
	if err != nil {
		t.Fatalf("first PrepareFixture() error = %v", err)
	}
	second, err := runner.PrepareFixture(ctx, cfg)
	if err != nil {
		t.Fatalf("second PrepareFixture() error = %v", err)
	}

	if first.FixtureName == second.FixtureName {
		t.Fatalf("generated fixture reused %q", first.FixtureName)
	}
	for _, fixture := range []string{first.FixtureName, second.FixtureName} {
		if !strings.HasPrefix(fixture, DefaultFixtureName+"-") {
			t.Fatalf("generated fixture = %q, want %q prefix", fixture, DefaultFixtureName+"-")
		}
	}
}

func TestRunJSONLMeasuresResolveAndContentRange(t *testing.T) {
	ctx := context.Background()
	baseURL, mockCAS := newTestDaemonWithCAS(t)
	runner := NewRunner(baseURL)
	arcs := newTestReadbenchArcs(ctx, t, mockCAS)

	var out bytes.Buffer
	err := runner.RunJSONL(ctx, RunConfig{
		Systems: []SystemName{SystemMALTFlat},
		Fixture: FixtureConfig{
			FixtureName:    "readbench-run",
			DirectoryDepth: 2,
			SmallFileBytes: 48,
			LargeFileBytes: 300 * 1024,
			Arcs:           arcs,
		},
		RangeHeader: "bytes=7-19",
		Iterations:  1,
	}, &out)
	if err != nil {
		t.Fatalf("RunJSONL() error = %v", err)
	}

	got := decodeJSONLResults(t, out.Bytes())
	if len(got) != 2 {
		t.Fatalf("result count = %d, want 2\n%s", len(got), out.String())
	}

	proofRead := got[0]
	if proofRead.System != SystemMALTFlat {
		t.Fatalf("first system = %q, want %q", proofRead.System, SystemMALTFlat)
	}
	if proofRead.OperationKind != OperationResolvePath {
		t.Fatalf("first operation = %q, want %q", proofRead.OperationKind, OperationResolvePath)
	}
	if proofRead.FixtureName != "readbench-run" || proofRead.Path != "dir00/dir01/small.txt" {
		t.Fatalf("proof read target = fixture %q path %q", proofRead.FixtureName, proofRead.Path)
	}
	if proofRead.RangeHeader != "" {
		t.Fatalf("prooflist range header = %q, want empty", proofRead.RangeHeader)
	}
	if proofRead.ElapsedNS <= 0 {
		t.Fatalf("prooflist elapsed_ns = %d, want > 0", proofRead.ElapsedNS)
	}
	if proofRead.ContentBytes != nil {
		t.Fatalf("prooflist content_bytes = %v, want omitted", *proofRead.ContentBytes)
	}
	if proofRead.ProofListStepCount == 0 {
		t.Fatal("prooflist step count should be recorded")
	}
	if proofRead.EvidenceItemCount != proofRead.ProofListStepCount {
		t.Fatalf("prooflist evidence items = %d, want prooflist step count %d", proofRead.EvidenceItemCount, proofRead.ProofListStepCount)
	}
	if proofRead.Proof.ProofListCount != 1 {
		t.Fatalf("prooflist proof metric count = %d, want 1", proofRead.Proof.ProofListCount)
	}
	if proofRead.Proof.StepCount != uint64(proofRead.ProofListStepCount) {
		t.Fatalf("prooflist proof metric steps = %d, result steps = %d", proofRead.Proof.StepCount, proofRead.ProofListStepCount)
	}
	if proofRead.ArcTable.GetCount+proofRead.ArcTable.BatchGetCount == 0 {
		t.Fatalf("prooflist arctable metrics should include reads: %+v", proofRead.ArcTable)
	}

	rangeRead := got[1]
	if rangeRead.System != SystemMALTFlat {
		t.Fatalf("second system = %q, want %q", rangeRead.System, SystemMALTFlat)
	}
	if rangeRead.OperationKind != OperationContentRange {
		t.Fatalf("second operation = %q, want %q", rangeRead.OperationKind, OperationContentRange)
	}
	if rangeRead.Path != "dir00/dir01/large.bin" {
		t.Fatalf("content range path = %q", rangeRead.Path)
	}
	if rangeRead.RangeHeader != "bytes=7-19" {
		t.Fatalf("range header = %q", rangeRead.RangeHeader)
	}
	if rangeRead.ContentBytes == nil || *rangeRead.ContentBytes != 13 {
		t.Fatalf("content bytes = %v, want 13", rangeRead.ContentBytes)
	}
	if rangeRead.ProofListStepCount == 0 {
		t.Fatal("content range prooflist step count should be recorded")
	}
	if rangeRead.EvidenceItemCount != rangeRead.ProofListStepCount {
		t.Fatalf("content range evidence items = %d, want prooflist step count %d", rangeRead.EvidenceItemCount, rangeRead.ProofListStepCount)
	}
	if rangeRead.Proof.ProofListCount != 1 {
		t.Fatalf("content proof metric count = %d, want reset per operation", rangeRead.Proof.ProofListCount)
	}
	if rangeRead.CAS.GetCount == 0 || rangeRead.CAS.BytesGet == 0 {
		t.Fatalf("content range CAS metrics should include bytes fetched: %+v", rangeRead.CAS)
	}
}

func TestRunJSONLEmitsUnixFSBaselines(t *testing.T) {
	ctx := context.Background()
	runner := NewRunner("http://127.0.0.1:0")

	var out bytes.Buffer
	err := runner.RunJSONL(ctx, RunConfig{
		Systems: []SystemName{SystemMerkleDAG, SystemHAMT},
		Fixture: FixtureConfig{
			FixtureName:    "readbench-baseline",
			DirectoryDepth: 2,
			SmallFileBytes: 48,
			LargeFileBytes: 300 * 1024,
		},
		RangeHeader: "bytes=7-19",
		Iterations:  1,
	}, &out)
	if err != nil {
		t.Fatalf("RunJSONL() error = %v", err)
	}

	got := decodeJSONLResults(t, out.Bytes())
	if len(got) != 4 {
		t.Fatalf("result count = %d, want 4\n%s", len(got), out.String())
	}

	wantSystems := []SystemName{SystemMerkleDAG, SystemMerkleDAG, SystemHAMT, SystemHAMT}
	for i, result := range got {
		if result.System != wantSystems[i] {
			t.Fatalf("record %d system = %q, want %q", i, result.System, wantSystems[i])
		}
		if result.FixtureName != "readbench-baseline" {
			t.Fatalf("record %d fixture = %q", i, result.FixtureName)
		}
		if result.CAS.GetCount == 0 || result.CAS.BytesGet == 0 {
			t.Fatalf("record %d should include baseline CAS reads: %+v", i, result.CAS)
		}
		if result.EvidenceItemCount != int(result.CAS.GetCount) {
			t.Fatalf("record %d evidence items = %d, want CAS get count %d", i, result.EvidenceItemCount, result.CAS.GetCount)
		}
		if result.ProofListStepCount != 0 {
			t.Fatalf("record %d prooflist steps = %d, want 0 for IPLD baseline", i, result.ProofListStepCount)
		}
		if result.ArcTable != (metrics.ArcTableStats{}) {
			t.Fatalf("record %d arctable metrics = %+v, want zero baseline metrics", i, result.ArcTable)
		}
		if result.Proof != (metrics.ProofStats{}) {
			t.Fatalf("record %d proof metrics = %+v, want zero baseline metrics", i, result.Proof)
		}
	}

	resolveRecords := []Result{got[0], got[2]}
	for _, result := range resolveRecords {
		if result.OperationKind != OperationResolvePath {
			t.Fatalf("%s first operation = %q, want %q", result.System, result.OperationKind, OperationResolvePath)
		}
		if result.Path != "dir00/dir01/small.txt" {
			t.Fatalf("%s resolve path = %q", result.System, result.Path)
		}
		if result.Target == "" {
			t.Fatalf("%s resolve target should be recorded", result.System)
		}
		if result.ContentBytes != nil {
			t.Fatalf("%s resolve content bytes = %v, want omitted", result.System, *result.ContentBytes)
		}
	}

	rangeRecords := []Result{got[1], got[3]}
	for _, result := range rangeRecords {
		if result.OperationKind != OperationContentRange {
			t.Fatalf("%s second operation = %q, want %q", result.System, result.OperationKind, OperationContentRange)
		}
		if result.Path != "dir00/dir01/large.bin" {
			t.Fatalf("%s range path = %q", result.System, result.Path)
		}
		if result.RangeHeader != "bytes=7-19" {
			t.Fatalf("%s range header = %q", result.System, result.RangeHeader)
		}
		if result.ContentBytes == nil || *result.ContentBytes != 13 {
			t.Fatalf("%s content bytes = %v, want 13", result.System, result.ContentBytes)
		}
	}
}

func TestNormalizeRunConfigDefaultsToAllSystems(t *testing.T) {
	got, err := normalizeRunConfig(RunConfig{
		Fixture: FixtureConfig{LargeFileBytes: 300 * 1024},
	})
	if err != nil {
		t.Fatalf("normalizeRunConfig() error = %v", err)
	}
	want := []SystemName{SystemMALTFlat, SystemMerkleDAG, SystemHAMT}
	if !reflect.DeepEqual(got.Systems, want) {
		t.Fatalf("systems = %q, want %q", got.Systems, want)
	}
}

func TestNormalizeRunConfigRejectsUnknownSystem(t *testing.T) {
	_, err := normalizeRunConfig(RunConfig{
		Systems: []SystemName{"unknown"},
		Fixture: FixtureConfig{LargeFileBytes: 300 * 1024},
	})
	if err == nil {
		t.Fatal("expected unknown system to fail")
	}
}

func TestNormalizeRunConfigRejectsDuplicateSystem(t *testing.T) {
	_, err := normalizeRunConfig(RunConfig{
		Systems: []SystemName{SystemMALTFlat, SystemMALTFlat},
		Fixture: FixtureConfig{LargeFileBytes: 300 * 1024},
	})
	if err == nil {
		t.Fatal("expected duplicate system to fail")
	}
}

func TestRunJSONLZeroIterationsSkipsFixtureSetup(t *testing.T) {
	ctx := context.Background()
	runner := NewRunner("http://127.0.0.1:0")

	var out bytes.Buffer
	err := runner.RunJSONL(ctx, RunConfig{
		Fixture: FixtureConfig{
			FixtureName:    "readbench-zero-setup",
			LargeFileBytes: 300 * 1024,
		},
		Iterations: 0,
	}, &out)
	if err != nil {
		t.Fatalf("RunJSONL() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("RunJSONL() wrote %q, want no records", out.String())
	}
}

func TestRunJSONLAllowsZeroIterations(t *testing.T) {
	ctx := context.Background()
	baseURL, mockCAS := newTestDaemonWithCAS(t)
	runner := NewRunner(baseURL)
	arcs := newTestReadbenchArcs(ctx, t, mockCAS)

	var out bytes.Buffer
	err := runner.RunJSONL(ctx, RunConfig{
		Fixture: FixtureConfig{
			FixtureName:    "readbench-zero-iterations",
			DirectoryDepth: 1,
			SmallFileBytes: 8,
			LargeFileBytes: 300 * 1024,
			Arcs:           arcs,
		},
		Iterations: 0,
	}, &out)
	if err != nil {
		t.Fatalf("RunJSONL() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("RunJSONL() wrote %q, want no records", out.String())
	}
}

func decodeJSONLResults(t *testing.T, data []byte) []Result {
	t.Helper()

	var out []Result
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var result Result
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			t.Fatalf("decode JSONL result %q: %v", scanner.Text(), err)
		}
		out = append(out, result)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	return out
}

func newTestDaemon(t *testing.T) string {
	t.Helper()
	baseURL, _ := newTestDaemonWithCAS(t)
	return baseURL
}

func newTestDaemonWithCAS(t *testing.T) (string, *casmock.CAS) {
	t.Helper()

	mockCAS := casmock.NewCAS(casmock.WithoutLatency())

	cfg := config.DefaultConfig()
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
	return ts.URL, mockCAS
}

func newTestReadbenchArcs(ctx context.Context, t *testing.T, mockCAS *casmock.CAS) map[string]string {
	t.Helper()
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
	return map[string]string{
		"@payload": manifestCID.String(),
		"dummy":    dummyCID.String(),
	}
}
