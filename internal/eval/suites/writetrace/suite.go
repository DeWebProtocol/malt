package writetrace

import (
	"context"
	"encoding/json"

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

	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreMode(cfg.StoreMode),
		Backend: evalstore.StoreBackend(cfg.StoreBackend),
		RootDir: cfg.StoreDir,
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
		RepoURL:     cfg.RepoURL,
		RepoPath:    cfg.RepoPath,
		CacheDir:    cfg.CacheDir,
		Ref:         cfg.RepoRef,
		Limit:       cfg.CommitLimit,
		FirstParent: cfg.FirstParent,
	}
	return source.Walk(ctx, func(commit replay.CommitMutation) error {
		return replay.RunCommitRecords(ctx, commit, systems, func(record replay.ResultRecord) error {
			return env.WriteRecord(SuiteName, record)
		})
	})
}

var _ framework.Suite = Suite{}
