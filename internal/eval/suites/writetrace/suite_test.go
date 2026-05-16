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

func TestParseConfigSupportsRepositoryListWithInheritedDefaults(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_ref": "main",
		"commit_limit": 10,
		"cache_dir": "/tmp/shared-cache",
		"first_parent": true,
		"repositories": [
			{
				"name": "alpha",
				"repo_url": "https://example.test/alpha.git"
			},
			{
				"name": "beta",
				"repo_path": "/tmp/beta",
				"repo_ref": "release",
				"commit_limit": 0,
				"cache_dir": "/tmp/beta-cache",
				"first_parent": false
			}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	repos, err := cfg.RepositoriesOrSingle()
	if err != nil {
		t.Fatalf("RepositoriesOrSingle: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("repo count = %d, want 2", len(repos))
	}
	if repos[0].Name != "alpha" || repos[0].RepoURL != "https://example.test/alpha.git" {
		t.Fatalf("repo 0 identity = %+v", repos[0])
	}
	if repos[0].RepoRef != "main" || repos[0].CommitLimit != 10 || repos[0].CacheDir != "/tmp/shared-cache" || !repos[0].FirstParent {
		t.Fatalf("repo 0 inherited defaults = %+v", repos[0])
	}
	if repos[1].Name != "beta" || repos[1].RepoPath != "/tmp/beta" {
		t.Fatalf("repo 1 identity = %+v", repos[1])
	}
	if repos[1].RepoRef != "release" || repos[1].CommitLimit != 0 || repos[1].CacheDir != "/tmp/beta-cache" || repos[1].FirstParent {
		t.Fatalf("repo 1 overrides = %+v", repos[1])
	}
}

func TestParseConfigKeepsSingleRepositoryCompatibility(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_path": "/tmp/single",
		"repo_ref": "HEAD",
		"commit_limit": 3
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	repos, err := cfg.RepositoriesOrSingle()
	if err != nil {
		t.Fatalf("RepositoriesOrSingle: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repo count = %d, want 1", len(repos))
	}
	if repos[0].RepoPath != "/tmp/single" || repos[0].RepoRef != "HEAD" || repos[0].CommitLimit != 3 {
		t.Fatalf("single repo = %+v", repos[0])
	}
}

func TestParseConfigRejectsRepositoryListMixedWithSingleRepoFields(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_path": "/tmp/single",
		"repositories": [
			{"repo_path": "/tmp/other"}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if _, err := cfg.RepositoriesOrSingle(); err == nil {
		t.Fatal("RepositoriesOrSingle should reject repositories mixed with repo_path")
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

func TestSuiteRunReplaysRepositoryListWithAliases(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repoA := initWriteTraceRepo(t)
	repoB := initWriteTraceRepo(t)
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "raw"), 0755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}

	cfg := json.RawMessage(`{
		"store_backend": "memory",
		"systems": ["maltflat"],
		"repositories": [
			{"name": "alpha", "repo_path": ` + strconvQuote(repoA) + `, "commit_limit": 1},
			{"name": "beta", "repo_path": ` + strconvQuote(repoB) + `, "commit_limit": 1}
		]
	}`)
	err := (writetrace.Suite{}).Run(ctx, framework.Env{
		RunID:     "run-write-trace-repos-test",
		OutputDir: outDir,
	}, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	envelopes := readWriteTraceEnvelopes(t, filepath.Join(outDir, "raw", "write_trace.jsonl"))
	if len(envelopes) != 2 {
		t.Fatalf("envelope count = %d, want 2", len(envelopes))
	}
	var records []replay.ResultRecord
	for i, envelope := range envelopes {
		var record replay.ResultRecord
		if err := json.Unmarshal(envelope.Record, &record); err != nil {
			t.Fatalf("unmarshal record %d: %v", i, err)
		}
		records = append(records, record)
	}
	if records[0].Repo != "alpha" || records[0].Index != 0 {
		t.Fatalf("record 0 = %+v, want alpha index 0", records[0])
	}
	if records[1].Repo != "beta" || records[1].Index != 0 {
		t.Fatalf("record 1 = %+v, want beta index 0", records[1])
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
