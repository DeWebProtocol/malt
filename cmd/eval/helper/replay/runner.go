package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// RunJSONL replays commits across systems and writes one JSON record per
// system per commit.
func RunJSONL(ctx context.Context, commits []CommitMutation, systems []SystemAdapter, w io.Writer) error {
	if w == nil {
		return fmt.Errorf("output writer is nil")
	}
	if len(systems) == 0 {
		return fmt.Errorf("at least one system is required")
	}
	enc := json.NewEncoder(w)
	for _, commit := range commits {
		if err := RunCommit(ctx, commit, systems, enc); err != nil {
			return err
		}
	}
	return nil
}

// RunCommit applies one commit to every system and emits JSON records.
func RunCommit(ctx context.Context, commit CommitMutation, systems []SystemAdapter, enc *json.Encoder) error {
	if enc == nil {
		return fmt.Errorf("json encoder is nil")
	}
	return RunCommitRecords(ctx, commit, systems, func(record ResultRecord) error {
		if err := enc.Encode(record); err != nil {
			return fmt.Errorf("encode %s commit %s: %w", record.System, record.Commit, err)
		}
		return nil
	})
}

// RunCommitRecords applies one commit to every system and emits structured
// records through emit. Callers that need framework-specific envelopes can
// write records directly without JSON encode/decode round trips.
func RunCommitRecords(ctx context.Context, commit CommitMutation, systems []SystemAdapter, emit func(ResultRecord) error) error {
	if emit == nil {
		return fmt.Errorf("record emitter is nil")
	}
	if len(systems) == 0 {
		return fmt.Errorf("at least one system is required")
	}
	for _, system := range systems {
		if system == nil {
			return fmt.Errorf("system adapter is nil")
		}
		result, err := system.Apply(ctx, commit)
		if err != nil {
			return fmt.Errorf("%s apply commit %s: %w", system.Name(), commit.Commit, err)
		}
		record := ResultRecord{
			Repo:        commit.Repo,
			System:      system.Name(),
			Commit:      commit.Commit,
			Parent:      commit.Parent,
			Index:       commit.Index,
			LiveStats:   commit.LiveStats,
			MutationSet: commit.Mutations,
			Skipped:     commit.Skipped,
			Result:      result,
			Accounting:  result.Accounting,
		}
		if err := emit(record); err != nil {
			return fmt.Errorf("emit %s commit %s: %w", system.Name(), commit.Commit, err)
		}
	}
	return nil
}
