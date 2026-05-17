package writetrace_test

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/maltflat"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	"github.com/dewebprotocol/malt/internal/eval/framework"
	"github.com/dewebprotocol/malt/internal/eval/suites/writetrace"
)

func TestSuiteName(t *testing.T) {
	if got := (writetrace.Suite{}).Name(); got != "write_trace" {
		t.Fatalf("suite name = %q, want write_trace", got)
	}
}

func TestParseConfigAppliesWriteCommandDefaultsAndJSONOverrides(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_url": "https://example.test/repo.git",
		"repo_path": "/tmp/repo",
		"repo_ref": "main",
		"commit_limit": 7,
		"cache_dir": "/tmp/cache",
		"store_dir": "/tmp/stores",
		"store_mode": "shared",
		"store_backend": "fs",
		"systems": ["maltflat", "hamt"],
		"first_parent": false
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.RepoURL != "https://example.test/repo.git" || cfg.RepoPath != "/tmp/repo" || cfg.RepoRef != "main" {
		t.Fatalf("repo config = %+v, want JSON values", cfg)
	}
	if cfg.CommitLimit != 7 || cfg.CacheDir != "/tmp/cache" || cfg.StoreDir != "/tmp/stores" {
		t.Fatalf("storage/limit config = %+v, want JSON values", cfg)
	}
	if cfg.StoreMode != "shared" || cfg.StoreBackend != "fs" || cfg.FirstParent {
		t.Fatalf("mode config = %+v, want JSON values", cfg)
	}
	if got := cfg.SystemsCSV(); got != "maltflat,hamt" {
		t.Fatalf("systems CSV = %q, want maltflat,hamt", got)
	}

	defaults, err := writetrace.ParseConfig(nil)
	if err != nil {
		t.Fatalf("ParseConfig defaults: %v", err)
	}
	if defaults.RepoRef != "HEAD" || defaults.CacheDir != ".eval-cache/repos" || defaults.StoreDir != ".eval-cache/write-stores" {
		t.Fatalf("defaults = %+v, want write command paths/ref", defaults)
	}
	if defaults.StoreMode != "isolated" || defaults.StoreBackend != "memory" || !defaults.FirstParent {
		t.Fatalf("defaults = %+v, want isolated memory first-parent", defaults)
	}
	if got := defaults.SystemsCSV(); got != "maltflat,merkledag,hamt" {
		t.Fatalf("default systems = %q, want maltflat,merkledag,hamt", got)
	}
}

func TestParseConfigRejectsUnknownFields(t *testing.T) {
	if _, err := writetrace.ParseConfig(json.RawMessage(`{"commit_limti": 7}`)); err == nil {
		t.Fatal("ParseConfig should reject unknown fields")
	}
}

func TestSuiteRunWritesFrameworkEnvelopedReplayRecords(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repo := initWriteTraceRepo(t)
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "raw"), 0755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}

	cfg := json.RawMessage(`{
		"repo_path": ` + strconvQuote(repo) + `,
		"commit_limit": 3,
		"store_backend": "memory",
		"systems": ["maltflat"]
	}`)
	err := (writetrace.Suite{}).Run(ctx, framework.Env{
		RunID:     "run-write-trace-test",
		OutputDir: outDir,
	}, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	envelopes := readWriteTraceEnvelopes(t, filepath.Join(outDir, "raw", "write_trace.jsonl"))
	if len(envelopes) != 3 {
		t.Fatalf("envelope count = %d, want 3", len(envelopes))
	}
	var records []replay.ResultRecord
	for i, envelope := range envelopes {
		if envelope.RunID != "run-write-trace-test" || envelope.Suite != "write_trace" {
			t.Fatalf("envelope %d = %+v, want run id and suite preserved", i, envelope)
		}
		var record replay.ResultRecord
		if err := json.Unmarshal(envelope.Record, &record); err != nil {
			t.Fatalf("unmarshal record %d: %v", i, err)
		}
		records = append(records, record)
	}

	if records[0].Repo != filepath.Base(repo) || records[0].System != "maltflat" || records[0].Index != 0 {
		t.Fatalf("first record identity = %+v, want repo/maltflat/index 0", records[0])
	}
	if records[1].MutationSet[0].Kind != replay.MutationRename || records[1].MutationSet[0].OldPath != "README.md" || records[1].MutationSet[0].Path != "docs/README.md" {
		t.Fatalf("rename mutation = %+v, want README.md -> docs/README.md", records[1].MutationSet)
	}
	if records[2].MutationSet[0].Kind != replay.MutationDelete || records[2].Result.MaterializedPaths != 0 {
		t.Fatalf("delete record = %+v, want delete with zero materialized paths", records[2])
	}
	for i, record := range records {
		if record.Commit == "" {
			t.Fatalf("record %d commit is empty", i)
		}
		if record.Result.MaterializationStrategy != maltflat.MaterializationStrategyLiveSnapshotRebuild {
			t.Fatalf("record %d materialization strategy = %q, want %q", i, record.Result.MaterializationStrategy, maltflat.MaterializationStrategyLiveSnapshotRebuild)
		}
		if record.Result.Accounting.Categories == nil || record.Accounting.Categories == nil {
			t.Fatalf("record %d accounting missing: %+v", i, record)
		}
	}
}

func initWriteTraceRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "bench@example.test")
	runGit(t, repo, "config", "user.name", "Bench Test")

	writeFile(t, repo, "README.md", "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")

	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.Rename(filepath.Join(repo, "README.md"), filepath.Join(repo, "docs", "README.md")); err != nil {
		t.Fatalf("rename README.md: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "rename readme")

	if err := os.Remove(filepath.Join(repo, "docs", "README.md")); err != nil {
		t.Fatalf("delete docs/README.md: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "delete readme")
	return repo
}

func readWriteTraceEnvelopes(t *testing.T, path string) []framework.RecordEnvelope {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var envelopes []framework.RecordEnvelope
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var envelope framework.RecordEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		envelopes = append(envelopes, envelope)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan envelopes: %v", err)
	}
	return envelopes
}

func writeFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func strconvQuote(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(data)
}
