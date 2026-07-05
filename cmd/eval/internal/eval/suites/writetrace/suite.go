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
	rawPath := env.RawPath(SuiteName)
	if cfg.Resume {
		if err := repairRawTail(rawPath); err != nil {
			return err
		}
	}
	tasks := buildTasks(cfg, repositories, repoPaths)
	progress, err := loadRawProgress(rawPath)
	if err != nil {
		return err
	}
	writer := &recordWriter{env: env}
	if cfg.Jobs == 1 {
		err = runTasksSerial(ctx, env, cfg, tasks, progress, writer, log)
	} else {
		err = runTasksParallel(ctx, env, cfg, tasks, progress, writer, log)
	}
	if err != nil {
		return err
	}
	rows, err := aggregateRawRecords(rawPath)
	if err != nil {
		return err
	}
	return writeAggregateCSV(env, rows)
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

func runRepoTask(ctx context.Context, env framework.Env, cfg Config, task repoReplayTask, progress map[taskKey]taskProgress, writer *recordWriter, log func(string, ...any)) (err error) {
	progresses, complete, allComplete, err := loadRepoTaskProgress(env, cfg, task, progress)
	if err != nil {
		return err
	}
	if allComplete {
		return nil
	}
	storeRoot := storeDirForRepo(env.WorkPath("write-stores"), task)
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

	systems, err := evalwrite.BuildSystems(ctx, factory, strings.Join(task.systems, ","))
	if err != nil {
		return err
	}

	source := gittrace.Source{
		RepoURL:     task.repo.RepoURL,
		RepoPath:    task.repoPath,
		Ref:         "HEAD",
		Limit:       cfg.MaxCommitsPerRepo,
		FirstParent: cfg.FirstParent,
	}
	err = source.Walk(ctx, func(commit replay.CommitMutation) error {
		commit.Repo = task.repo.RepoID
		for _, system := range systems {
			name := system.Name()
			result, err := system.Apply(ctx, commit)
			if err != nil {
				return fmt.Errorf("%s apply commit %s: %w", name, commit.Commit, err)
			}
			progress := progresses[name]
			if cfg.Resume && progress.Index >= 0 && commit.Index <= progress.Index {
				if err := verifyWarmProgress(task, name, progress, commit, result); err != nil {
					return err
				}
				continue
			}
			if complete[name] {
				continue
			}
			record := resultRecord(commit, name, result)
			if err := writer.Write(record); err != nil {
				return err
			}
			next := taskProgress{
				Repo:   record.Repo,
				System: record.System,
				Index:  record.Index,
				Commit: record.Commit,
				Root:   record.Result.Root,
			}
			progresses[name] = next
			if err := saveCheckpoint(task.checkpointPath(env, name), next); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, system := range systems {
		name := system.Name()
		last := progresses[name]
		if last.Repo == "" {
			last = initialTaskProgress(task.repo.RepoID, name)
		}
		last.Complete = true
		if err := saveCheckpoint(task.checkpointPath(env, name), last); err != nil {
			return err
		}
	}
	return nil
}

var _ framework.Suite = Suite{}

func loadRepoTaskProgress(env framework.Env, cfg Config, task repoReplayTask, rawProgress map[taskKey]taskProgress) (map[string]taskProgress, map[string]bool, bool, error) {
	progresses := make(map[string]taskProgress, len(task.systems))
	complete := make(map[string]bool, len(task.systems))
	allComplete := cfg.Resume && len(task.systems) > 0
	for _, system := range task.systems {
		progress := rawProgress[taskKey{repo: task.repo.RepoID, system: system}]
		if progress.Repo == "" {
			progress = initialTaskProgress(task.repo.RepoID, system)
		}
		if cfg.Resume {
			checkpoint, found, err := loadCheckpoint(task.checkpointPath(env, system))
			if err != nil {
				return nil, nil, false, err
			}
			if found {
				progress, err = mergeProgress(progress, checkpoint)
				if err != nil {
					return nil, nil, false, err
				}
			}
		}
		progresses[system] = progress
		complete[system] = progress.Complete
		if !progress.Complete {
			allComplete = false
		}
	}
	return progresses, complete, allComplete, nil
}

func initialTaskProgress(repo, system string) taskProgress {
	return taskProgress{Repo: repo, System: system, Index: -1}
}

func mergeProgress(raw, checkpoint taskProgress) (taskProgress, error) {
	if checkpoint.Index == raw.Index {
		if raw.Commit != "" && checkpoint.Commit != "" && raw.Commit != checkpoint.Commit {
			return taskProgress{}, fmt.Errorf("checkpoint commit mismatch for %s/%s index %d: raw=%s checkpoint=%s",
				raw.Repo, raw.System, raw.Index, raw.Commit, checkpoint.Commit)
		}
		if raw.Root != "" && checkpoint.Root != "" && raw.Root != checkpoint.Root {
			return taskProgress{}, fmt.Errorf("checkpoint root mismatch for %s/%s index %d: raw=%s checkpoint=%s",
				raw.Repo, raw.System, raw.Index, raw.Root, checkpoint.Root)
		}
		if raw.Root == "" {
			raw.Root = checkpoint.Root
		}
		raw.Complete = checkpoint.Complete
		return raw, nil
	}
	if checkpoint.Complete || checkpoint.Index > raw.Index {
		return checkpoint, nil
	}
	return raw, nil
}

func verifyWarmProgress(task repoReplayTask, system string, progress taskProgress, commit replay.CommitMutation, result replay.ApplyResult) error {
	if commit.Index != progress.Index {
		return nil
	}
	if progress.Commit != "" && progress.Commit != commit.Commit {
		return fmt.Errorf("checkpoint commit mismatch for %s/%s index %d: checkpoint=%s replay=%s",
			task.repo.RepoID, system, commit.Index, progress.Commit, commit.Commit)
	}
	if progress.Root != "" && result.Root != progress.Root {
		return fmt.Errorf("checkpoint root mismatch for %s/%s index %d: checkpoint=%s replay=%s",
			task.repo.RepoID, system, commit.Index, progress.Root, result.Root)
	}
	return nil
}

func resultRecord(commit replay.CommitMutation, system string, result replay.ApplyResult) replay.ResultRecord {
	logicalChangedPayloadBytes := replay.LogicalChangedPayloadBytes(commit.Mutations)
	physicalPersistedBytes := result.AccountingDelta.Total.NewPersistedBytes
	physicalPayloadBytes := result.AccountingDelta.Categories[evalstore.CategoryCASPayload].NewPersistedBytes
	physicalMetadataBytes := physicalPersistedBytes
	if physicalMetadataBytes >= physicalPayloadBytes {
		physicalMetadataBytes -= physicalPayloadBytes
	} else {
		physicalMetadataBytes = 0
	}
	var writeAmplification *float64
	if logicalChangedPayloadBytes > 0 {
		value := float64(physicalPersistedBytes) / float64(logicalChangedPayloadBytes)
		writeAmplification = &value
	}
	return replay.ResultRecord{
		Repo:                       commit.Repo,
		System:                     system,
		Commit:                     commit.Commit,
		Parent:                     commit.Parent,
		Index:                      commit.Index,
		LiveStats:                  commit.LiveStats,
		MutationSet:                commit.Mutations,
		Skipped:                    commit.Skipped,
		Result:                     result,
		Accounting:                 result.Accounting,
		AccountingDelta:            result.AccountingDelta,
		LogicalChangedPayloadBytes: logicalChangedPayloadBytes,
		PhysicalPersistedBytes:     physicalPersistedBytes,
		PhysicalPayloadBytes:       physicalPayloadBytes,
		PhysicalMetadataBytes:      physicalMetadataBytes,
		WriteAmplification:         writeAmplification,
	}
}

func (t repoReplayTask) checkpointPath(env framework.Env, system string) string {
	return filepath.Join(env.WorkPath("write-checkpoints"), t.repo.StoreName(t.repoIndex), system+".json")
}

func storeDirForRepo(base string, task repoReplayTask) string {
	if base == "" {
		return base
	}
	return filepath.Join(base, task.repo.StoreName(task.repoIndex))
}
