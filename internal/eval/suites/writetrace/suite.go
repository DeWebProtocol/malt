package writetrace

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/dewebprotocol/malt/cmd/eval/helper/evalwrite"
	gittrace "github.com/dewebprotocol/malt/cmd/eval/helper/git"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/eval/framework"
)

// Suite replays Git commit traces and writes framework raw records.
type Suite struct{}

func (Suite) Name() string {
	return SuiteName
}

func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) (err error) {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	repositories, err := cfg.RepositoriesOrSingle()
	if err != nil {
		return err
	}

	for i, repo := range repositories {
		if err := runRepository(ctx, env, cfg, repo, i, len(repositories)); err != nil {
			return err
		}
	}
	return nil
}

func runRepository(ctx context.Context, env framework.Env, cfg Config, repo RepositoryConfig, index, repoCount int) (err error) {
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreMode(cfg.StoreMode),
		Backend: evalstore.StoreBackend(cfg.StoreBackend),
		RootDir: storeDirForRepository(cfg.StoreDir, repo, index, repoCount),
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := factory.Close(); err == nil {
			err = closeErr
		}
	}()

	systems, err := evalwrite.BuildSystems(ctx, factory, cfg.SystemsCSV())
	if err != nil {
		return err
	}

	source := gittrace.Source{
		RepoURL:     repo.RepoURL,
		RepoPath:    repo.RepoPath,
		CacheDir:    repo.CacheDir,
		Ref:         repo.RepoRef,
		Limit:       repo.CommitLimit,
		FirstParent: repo.FirstParent,
	}
	return source.Walk(ctx, func(commit replay.CommitMutation) error {
		if repo.Name != "" {
			commit.Repo = repo.Name
		}
		return replay.RunCommitRecords(ctx, commit, systems, func(record replay.ResultRecord) error {
			return env.WriteRecord(SuiteName, record)
		})
	})
}

var _ framework.Suite = Suite{}

func storeDirForRepository(base string, repo RepositoryConfig, index, repoCount int) string {
	if base == "" || repoCount <= 1 {
		return base
	}
	return filepath.Join(base, repo.StoreName(index))
}
