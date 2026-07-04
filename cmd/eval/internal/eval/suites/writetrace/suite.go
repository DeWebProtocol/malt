package writetrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

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

func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	log := env.Log()
	cfg, err := ParseConfig(raw)
	if err != nil {
		return err
	}
	if env.Resume {
		cfg.Resume = true
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	repositories, err := cfg.RepositoryTargets()
	if err != nil {
		return err
	}

	log("  repositories=%d systems=%s jobs=%d resume=%t", len(repositories), cfg.SystemsCSV(), cfg.Jobs, cfg.Resume)
	repoPaths, err := cloneRepositories(ctx, env, repositories)
	if err != nil {
		return err
	}
	progress, err := loadRawProgress(env.RawPath(SuiteName))
	if err != nil {
		return err
	}
	tasks := buildTasks(cfg, repositories, repoPaths)
	writer := &recordWriter{env: env}
	if cfg.Jobs == 1 {
		err = runTasksSerial(ctx, env, cfg, tasks, progress, writer, log)
	} else {
		err = runTasksParallel(ctx, env, cfg, tasks, progress, writer, log)
	}
	if err != nil {
		return err
	}
	rows, err := aggregateRawRecords(env.RawPath(SuiteName))
	if err != nil {
		return err
	}
	return writeAggregateCSV(env, rows)
}

type replayTask struct {
	repo      RepositoryTarget
	repoIndex int
	repoPath  string
	system    string
}

type repoReplayTask struct {
	repo      RepositoryTarget
	repoIndex int
	repoPath  string
	systems   []string
}

type taskKey struct {
	repo   string
	system string
}

type taskProgress struct {
	Repo     string `json:"repo"`
	System   string `json:"system"`
	Index    int    `json:"index"`
	Commit   string `json:"commit"`
	Root     string `json:"root,omitempty"`
	Complete bool   `json:"complete,omitempty"`
}

type recordWriter struct {
	mu  sync.Mutex
	env framework.Env
}

func (w *recordWriter) Write(record replay.ResultRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.env.WriteRecord(SuiteName, record)
}

func cloneRepositories(ctx context.Context, env framework.Env, repositories []RepositoryTarget) (map[string]string, error) {
	paths := make(map[string]string, len(repositories))
	for _, repo := range repositories {
		path, err := gittrace.CloneForReplay(ctx, repo.RepoURL, env.WorkPath("repos"))
		if err != nil {
			return nil, err
		}
		paths[repo.RepoID] = path
	}
	return paths, nil
}

func buildTasks(cfg Config, repositories []RepositoryTarget, repoPaths map[string]string) []repoReplayTask {
	systems := normalizeSystems(cfg.Systems)
	tasks := make([]repoReplayTask, 0, len(repositories))
	for i, repo := range repositories {
		tasks = append(tasks, repoReplayTask{
			repo:      repo,
			repoIndex: i,
			repoPath:  repoPaths[repo.RepoID],
			systems:   append([]string(nil), systems...),
		})
	}
	return tasks
}

func runTasksSerial(ctx context.Context, env framework.Env, cfg Config, tasks []repoReplayTask, progress map[taskKey]taskProgress, writer *recordWriter, log func(string, ...any)) error {
	var runErr error
	for i, task := range tasks {
		log("  task [%d/%d] repository=%s systems=%s", i+1, len(tasks), task.repo.RepoID, strings.Join(task.systems, ","))
		if err := runRepoTask(ctx, env, cfg, task, progress, writer, log); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
}

func runTasksParallel(ctx context.Context, env framework.Env, cfg Config, tasks []repoReplayTask, progress map[taskKey]taskProgress, writer *recordWriter, log func(string, ...any)) error {
	taskCh := make(chan repoReplayTask)
	errCh := make(chan error, len(tasks))
	var wg sync.WaitGroup
	for worker := 0; worker < cfg.Jobs; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for task := range taskCh {
				log("  worker=%d repository=%s systems=%s", worker+1, task.repo.RepoID, strings.Join(task.systems, ","))
				if err := runRepoTask(ctx, env, cfg, task, progress, writer, log); err != nil {
					errCh <- err
				}
			}
		}(worker)
	}
	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)
	wg.Wait()
	close(errCh)
	var runErr error
	for err := range errCh {
		runErr = errors.Join(runErr, err)
	}
	return runErr
}

func runRepoTask(ctx context.Context, env framework.Env, cfg Config, task repoReplayTask, progress map[taskKey]taskProgress, writer *recordWriter, log func(string, ...any)) error {
	var runErr error
	for _, system := range task.systems {
		systemTask := replayTask{
			repo:      task.repo,
			repoIndex: task.repoIndex,
			repoPath:  task.repoPath,
			system:    system,
		}
		log("    system=%s", system)
		if err := runTask(ctx, env, cfg, systemTask, progress[systemTask.key()], writer); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	return runErr
}

func runTask(ctx context.Context, env framework.Env, cfg Config, task replayTask, progress taskProgress, writer *recordWriter) (err error) {
	if progress.Repo == "" {
		progress = taskProgress{Repo: task.repo.RepoID, System: task.system, Index: -1}
	}
	checkpointPath := task.checkpointPath(env)
	if cfg.Resume {
		checkpoint, found, err := loadCheckpoint(checkpointPath)
		if err != nil {
			return err
		}
		if found && (checkpoint.Complete || checkpoint.Index > progress.Index) {
			progress = checkpoint
		}
		if progress.Complete {
			return nil
		}
	}
	storeRoot := storeDirForTask(env.WorkPath("write-stores"), task)
	if cfg.Resume {
		if err := evalstore.RemoveStoreDir(storeRoot); err != nil {
			return err
		}
	}
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreMode(cfg.StoreMode),
		Backend: evalstore.StoreBackend(cfg.StoreBackend),
		RootDir: storeRoot,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := factory.Close(); err == nil {
			err = closeErr
		}
	}()

	systems, err := evalwrite.BuildSystems(ctx, factory, task.system)
	if err != nil {
		return err
	}
	if len(systems) != 1 {
		return fmt.Errorf("write_trace task built %d systems, want 1", len(systems))
	}

	source := gittrace.Source{
		RepoURL:     task.repo.RepoURL,
		RepoPath:    task.repoPath,
		Ref:         "HEAD",
		Limit:       cfg.MaxCommitsPerRepo,
		FirstParent: cfg.FirstParent,
	}
	last := progress
	err = source.Walk(ctx, func(commit replay.CommitMutation) error {
		commit.Repo = task.repo.RepoID
		if cfg.Resume && progress.Index >= 0 && commit.Index <= progress.Index {
			if commit.Index == progress.Index && progress.Commit != "" && progress.Commit != commit.Commit {
				return fmt.Errorf("checkpoint commit mismatch for %s/%s index %d: checkpoint=%s replay=%s",
					task.repo.RepoID, task.system, commit.Index, progress.Commit, commit.Commit)
			}
			_, err := systems[0].Apply(ctx, commit)
			if err != nil {
				return err
			}
			last = taskProgress{Repo: task.repo.RepoID, System: task.system, Index: commit.Index, Commit: commit.Commit}
			return nil
		}
		return replay.RunCommitRecords(ctx, commit, systems, func(record replay.ResultRecord) error {
			if err := writer.Write(record); err != nil {
				return err
			}
			last = taskProgress{
				Repo:   record.Repo,
				System: record.System,
				Index:  record.Index,
				Commit: record.Commit,
				Root:   record.Result.Root,
			}
			return saveCheckpoint(checkpointPath, last)
		})
	})
	if err != nil {
		return err
	}
	if last.Repo == "" {
		last = taskProgress{Repo: task.repo.RepoID, System: task.system, Index: -1}
	}
	last.Complete = true
	return saveCheckpoint(checkpointPath, last)
}

var _ framework.Suite = Suite{}

func (t replayTask) key() taskKey {
	return taskKey{repo: t.repo.RepoID, system: t.system}
}

func (t replayTask) checkpointPath(env framework.Env) string {
	return filepath.Join(env.WorkPath("write-checkpoints"), t.repo.StoreName(t.repoIndex), t.system+".json")
}

func storeDirForTask(base string, task replayTask) string {
	if base == "" {
		return base
	}
	return filepath.Join(base, task.repo.StoreName(task.repoIndex), task.system)
}
