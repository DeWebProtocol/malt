package writetrace

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/dewebprotocol/malt/cmd/eval/helper/evalwrite"
	gittrace "github.com/dewebprotocol/malt/cmd/eval/helper/git"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
)

// Suite replays Git commit traces and writes framework raw records.
type Suite struct{}

func (Suite) Name() string {
	return SuiteName
}

func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) (err error) {
	log := env.Log()
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	repositories, err := cfg.RepositoryTargets()
	if err != nil {
		return err
	}

	log("  repositories=%d systems=%s", len(repositories), cfg.SystemsCSV())
	var allRecords []replay.ResultRecord
	for i, repo := range repositories {
		log("  [%d/%d] repository=%s", i+1, len(repositories), repo.RepoID)
		records, err := runRepository(ctx, env, cfg, repo, i, len(repositories))
		if err != nil {
			return err
		}
		allRecords = append(allRecords, records...)
	}
	return writeAggregateCSV(env, aggregateRecords(allRecords))
}

func runRepository(ctx context.Context, env framework.Env, cfg Config, repo RepositoryTarget, index, repoCount int) (records []replay.ResultRecord, err error) {
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreMode(cfg.StoreMode),
		Backend: evalstore.StoreBackend(cfg.StoreBackend),
		RootDir: storeDirForRepository(env.WorkPath("write-stores"), repo, index, repoCount),
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := factory.Close(); err == nil {
			err = closeErr
		}
	}()

	systems, err := evalwrite.BuildSystems(ctx, factory, cfg.SystemsCSV())
	if err != nil {
		return nil, err
	}

	source := gittrace.Source{
		RepoURL:      repo.RepoURL,
		CloneBaseDir: env.WorkPath("repos"),
		Ref:          "HEAD",
		Limit:        cfg.MaxCommitsPerRepo,
		FirstParent:  cfg.FirstParent,
	}
	err = source.Walk(ctx, func(commit replay.CommitMutation) error {
		commit.Repo = repo.RepoID
		return replay.RunCommitRecords(ctx, commit, systems, func(record replay.ResultRecord) error {
			records = append(records, record)
			return env.WriteRecord(SuiteName, record)
		})
	})
	return records, err
}

var _ framework.Suite = Suite{}

func storeDirForRepository(base string, repo RepositoryTarget, index, repoCount int) string {
	if base == "" || repoCount <= 1 {
		return base
	}
	return filepath.Join(base, repo.StoreName(index))
}
