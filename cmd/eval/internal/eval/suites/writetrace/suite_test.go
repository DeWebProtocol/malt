package writetrace_test

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/maltflat"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/writetrace"
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
	if cfg.MaxCommitsPerRepo != 7 {
		t.Fatalf("limit config = %+v, want JSON value", cfg)
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
		"first_parent": true,
		"repo_urls": [
			"https://github.com/ipfs/kubo.git",
			"https://github.com/ethereum/go-ethereum"
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
	if repos[0].RepoID != "github.com/ipfs/kubo" || repos[0].RepoURL != "https://github.com/ipfs/kubo.git" {
		t.Fatalf("repo 0 target = %+v", repos[0])
	}
	if repos[1].RepoID != "github.com/ethereum/go-ethereum" || repos[1].RepoURL != "https://github.com/ethereum/go-ethereum" {
		t.Fatalf("repo 1 target = %+v", repos[1])
	}
	if cfg.MaxCommitsPerRepo != 10 || !cfg.FirstParent {
		t.Fatalf("suite defaults = %+v", cfg)
	}
}

func TestParseConfigRejectsNonGitHubHTTPSRepoURLs(t *testing.T) {
	cases := []string{
		"git@github.com:ipfs/kubo.git",
		"file:///tmp/eval/repos/github.com/ipfs/kubo.git",
		"file:/c:%5cusers%5cadmini~1%5cappdata%5clocal%5ctemp%5c001%5cgithub.com%5cipfs%5ckubo",
		"http://github.com/ipfs/kubo.git",
		"https://gitlab.com/ipfs/kubo.git",
	}
	for _, repoURL := range cases {
		cfg, err := writetrace.ParseConfig(json.RawMessage(`{
			"repo_urls": [` + strconvQuote(repoURL) + `]
		}`))
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		if _, err := cfg.RepositoryTargets(); err == nil {
			t.Fatalf("RepositoryTargets should reject %q", repoURL)
		}
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
	if got := repos[0].StoreName(0); got != "000-github.com-ipfs-kubo" {
		t.Fatalf("repo 0 store name = %q", got)
	}
	if got := repos[1].StoreName(1); got != "001-github.com-fork-kubo" {
		t.Fatalf("repo 1 store name = %q", got)
	}
}

func TestParseConfigRejectsDuplicateCanonicalRepoIDs(t *testing.T) {
	cfg, err := writetrace.ParseConfig(json.RawMessage(`{
		"repo_urls": [
			"https://github.com/ipfs/kubo.git",
			"https://github.com/ipfs/kubo"
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
		`{"cache_dir": "/tmp/cache"}`,
		`{"store_dir": "/tmp/stores"}`,
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
	sourceBase := filepath.Join(t.TempDir(), "github.com")
	initWriteTraceRepoAt(t, filepath.Join(sourceBase, "ipfs", "kubo.git"))
	installGitHubInsteadOf(t, sourceBase)
	resultDir := t.TempDir()
	outputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(resultDir, "raw"), 0755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}

	cfg := json.RawMessage(`{
		"repo_urls": [` + strconvQuote(githubRepoURL("ipfs", "kubo")) + `],
		"max_commits_per_repo": 3,
		"store_backend": "memory",
		"systems": ["maltflat"]
	}`)
	err := (writetrace.Suite{}).Run(ctx, framework.Env{
		RunID:     "run-write-trace-test",
		OutputDir: outputDir,
		ResultDir: resultDir,
	}, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "repos", "ipfs", "kubo", ".git")); err != nil {
		t.Fatalf("managed clone should remain under output workspace: %v", err)
	}

	envelopes := readWriteTraceEnvelopes(t, filepath.Join(resultDir, "raw", "write_trace.jsonl"))
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

	if records[0].Repo != "github.com/ipfs/kubo" || records[0].System != "maltflat" || records[0].Index != 0 {
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
		if record.Result.MaterializationStrategy != maltflat.MaterializationStrategyIncrementalDelta {
			t.Fatalf("record %d materialization strategy = %q, want %q", i, record.Result.MaterializationStrategy, maltflat.MaterializationStrategyIncrementalDelta)
		}
		if record.Result.Accounting.Categories == nil || record.Accounting.Categories == nil {
			t.Fatalf("record %d accounting missing: %+v", i, record)
		}
		if record.AccountingDelta.Categories == nil {
			t.Fatalf("record %d accounting delta missing: %+v", i, record)
		}
	}

	aggregateRows := readAggregateRows(t, filepath.Join(resultDir, "aggregate", "write_trace.csv"))
	if len(aggregateRows) != 1 {
		t.Fatalf("aggregate row count = %d, want 1", len(aggregateRows))
	}
	row := aggregateRows[0]
	if row["repo"] != "github.com/ipfs/kubo" || row["system"] != "maltflat" || row["commits"] != "3" {
		t.Fatalf("aggregate identity = %+v, want repo/system/3 commits", row)
	}
	if row["logical_changed_payload_bytes"] == "" || row["physical_persisted_bytes"] == "" || row["cumulative_write_amplification"] == "" {
		t.Fatalf("aggregate missing write amplification fields: %+v", row)
	}
	if row["arctable_persisted_bytes"] == "" {
		t.Fatalf("aggregate missing ArcTable breakdown field: %+v", row)
	}
}

func TestSuiteRunReplaysRepoURLListWithCanonicalRepoLabels(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	sourceBase := filepath.Join(t.TempDir(), "github.com")
	initWriteTraceRepoAt(t, filepath.Join(sourceBase, "ipfs", "kubo.git"))
	initWriteTraceRepoAt(t, filepath.Join(sourceBase, "fork", "kubo.git"))
	installGitHubInsteadOf(t, sourceBase)
	resultDir := t.TempDir()
	outputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(resultDir, "raw"), 0755); err != nil {
		t.Fatalf("mkdir raw dir: %v", err)
	}

	cfg := json.RawMessage(`{
		"store_backend": "memory",
		"systems": ["maltflat"],
		"max_commits_per_repo": 1,
		"repo_urls": [
			` + strconvQuote(githubRepoURL("ipfs", "kubo")) + `,
			` + strconvQuote(githubRepoURL("fork", "kubo")) + `
		]
	}`)
	err := (writetrace.Suite{}).Run(ctx, framework.Env{
		RunID:     "run-write-trace-repos-test",
		OutputDir: outputDir,
		ResultDir: resultDir,
	}, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, path := range []string{
		filepath.Join(outputDir, "repos", "ipfs", "kubo", ".git"),
		filepath.Join(outputDir, "repos", "fork", "kubo", ".git"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("managed clone %s should remain under output workspace: %v", path, err)
		}
	}

	envelopes := readWriteTraceEnvelopes(t, filepath.Join(resultDir, "raw", "write_trace.jsonl"))
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
	if records[0].Repo != "github.com/ipfs/kubo" || records[0].Index != 0 {
		t.Fatalf("record 0 = %+v, want github.com/ipfs/kubo index 0", records[0])
	}
	if records[1].Repo != "github.com/fork/kubo" || records[1].Index != 0 {
		t.Fatalf("record 1 = %+v, want github.com/fork/kubo index 0", records[1])
	}
}

func initWriteTraceRepoAt(t *testing.T, repo string) string {
	t.Helper()
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

func installGitHubInsteadOf(t *testing.T, githubRoot string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "gitconfig")
	baseURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(githubRoot) + "/"}).String()
	data := []byte("[url \"" + baseURL + "\"]\n\tinsteadOf = https://github.com/\n")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("write git config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", configPath)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func githubRepoURL(owner, name string) string {
	return "https://github.com/" + owner + "/" + name + ".git"
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

func readAggregateRows(t *testing.T, path string) []map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open aggregate %s: %v", path, err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read aggregate csv: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("aggregate csv missing header")
	}
	header := rows[0]
	out := make([]map[string]string, 0, len(rows)-1)
	for _, row := range rows[1:] {
		mapped := make(map[string]string, len(header))
		for i, key := range header {
			if i < len(row) {
				mapped[key] = row[i]
			}
		}
		out = append(out, mapped)
	}
	return out
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
