package writetrace_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/url"
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
		"repo_urls": ["https://github.com/ipfs/kubo.git"],
		"max_commits_per_repo": 7,
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
	if len(cfg.RepoURLs) != 1 || cfg.RepoURLs[0] != "https://github.com/ipfs/kubo.git" {
		t.Fatalf("repo config = %+v, want JSON values", cfg)
	}
	if cfg.MaxCommitsPerRepo != 7 || cfg.CacheDir != "/tmp/cache" || cfg.StoreDir != "/tmp/stores" {
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
	if defaults.CacheDir != ".eval-cache/repos" || defaults.StoreDir != ".eval-cache/write-stores" {
		t.Fatalf("defaults = %+v, want write command paths/ref", defaults)
	}
	if defaults.StoreMode != "isolated" || defaults.StoreBackend != "memory" || !defaults.FirstParent {
		t.Fatalf("defaults = %+v, want isolated memory first-parent", defaults)
	}
	if got := defaults.SystemsCSV(); got != "maltflat,merkledag,hamt" {
		t.Fatalf("default systems = %q, want maltflat,merkledag,hamt", got)
	}
}

func TestParseConfigBuildsRepositoryTargetsFromURLList(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"max_commits_per_repo": 10,
		"cache_dir": "/tmp/shared-cache",
		"first_parent": true,
		"repo_urls": [
			"https://github.com/ipfs/kubo.git",
			"git@github.com:ethereum/go-ethereum.git"
		]
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	repos, err := cfg.RepositoryTargets()
	if err != nil {
		t.Fatalf("RepositoryTargets: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("repo count = %d, want 2", len(repos))
	}
	if repos[0].RepoID != "ipfs/kubo" || repos[0].RepoURL != "https://github.com/ipfs/kubo.git" {
		t.Fatalf("repo 0 target = %+v", repos[0])
	}
	if repos[1].RepoID != "ethereum/go-ethereum" || repos[1].RepoURL != "git@github.com:ethereum/go-ethereum.git" {
		t.Fatalf("repo 1 target = %+v", repos[1])
	}
	if cfg.MaxCommitsPerRepo != 10 || cfg.CacheDir != "/tmp/shared-cache" || !cfg.FirstParent {
		t.Fatalf("suite defaults = %+v", cfg)
	}
}

func TestRepositoryStoreNameUsesIndexedCanonicalRepoID(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_urls": [
			"https://github.com/ipfs/kubo.git",
			"https://github.com/fork/kubo.git"
		]
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	repos, err := cfg.RepositoryTargets()
	if err != nil {
		t.Fatalf("RepositoryTargets: %v", err)
	}
	if got := repos[0].StoreName(0); got != "000-ipfs-kubo" {
		t.Fatalf("repo 0 store name = %q", got)
	}
	if got := repos[1].StoreName(1); got != "001-fork-kubo" {
		t.Fatalf("repo 1 store name = %q", got)
	}
}

func TestParseConfigRejectsDuplicateCanonicalRepoIDs(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_urls": [
			"https://github.com/ipfs/kubo.git",
			"git@github.com:ipfs/kubo.git"
		]
	}`))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if _, err := cfg.RepositoryTargets(); err == nil {
		t.Fatal("RepositoryTargets should reject duplicate canonical repo IDs")
	}
}

func TestParseConfigRejectsUnknownFields(t *testing.T) {
	if _, err := writetrace.ParseConfig(json.RawMessage(`{"commit_limti": 7}`)); err == nil {
		t.Fatal("ParseConfig should reject unknown fields")
	}
}

func TestParseConfigRejectsLegacyRepositoryFields(t *testing.T) {
	for _, raw := range []string{
		`{"repo_url": "https://github.com/ipfs/kubo.git"}`,
		`{"repo_ref": "main"}`,
		`{"commit_limit": 7}`,
		`{"repositories": [{"repo_url": "https://github.com/ipfs/kubo.git"}]}`,
	} {
		if _, err := writetrace.ParseConfig(json.RawMessage(raw)); err == nil {
			t.Fatalf("ParseConfig should reject legacy field in %s", raw)
		}
	}
}

func TestSuiteRunWritesFrameworkEnvelopedReplayRecords(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repo := initWriteTraceRepo(t, "ipfs", "kubo.git")
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "raw"), 0755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	cacheDir := t.TempDir()

	cfg := json.RawMessage(`{
		"repo_urls": [` + strconvQuote(fileURL(repo)) + `],
		"max_commits_per_repo": 3,
		"cache_dir": ` + strconvQuote(cacheDir) + `,
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

	if records[0].Repo != "ipfs/kubo" || records[0].System != "maltflat" || records[0].Index != 0 {
		t.Fatalf("first record identity = %+v, want canonical repo/maltflat/index 0", records[0])
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

func TestSuiteRunReplaysRepoURLListWithCanonicalRepoLabels(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repoA := initWriteTraceRepo(t, "ipfs", "kubo.git")
	repoB := initWriteTraceRepo(t, "fork", "kubo.git")
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "raw"), 0755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}
	cacheDir := t.TempDir()

	cfg := json.RawMessage(`{
		"store_backend": "memory",
		"systems": ["maltflat"],
		"max_commits_per_repo": 1,
		"cache_dir": ` + strconvQuote(cacheDir) + `,
		"repo_urls": [
			` + strconvQuote(fileURL(repoA)) + `,
			` + strconvQuote(fileURL(repoB)) + `
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
	if records[0].Repo != "ipfs/kubo" || records[0].Index != 0 {
		t.Fatalf("record 0 = %+v, want ipfs/kubo index 0", records[0])
	}
	if records[1].Repo != "fork/kubo" || records[1].Index != 0 {
		t.Fatalf("record 1 = %+v, want fork/kubo index 0", records[1])
	}
}

func initWriteTraceRepo(t *testing.T, owner, repoName string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), owner, repoName)
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
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

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
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
