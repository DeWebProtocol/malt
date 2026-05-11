package replay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
)

type fakeAdapter struct {
	name  string
	roots []string
}

func (a *fakeAdapter) Name() string {
	return a.name
}

func (a *fakeAdapter) Apply(ctx context.Context, commit replay.CommitMutation) (replay.ApplyResult, error) {
	a.roots = append(a.roots, commit.Commit)
	return replay.ApplyResult{
		Root:              a.name + "-" + commit.Commit,
		AppliedMutations:  len(commit.Mutations),
		Accounting:        evalstore.NewMeter().Snapshot(),
		MaterializedPaths: len(commit.LiveFiles),
	}, nil
}

func TestRunJSONLEmitsOneRecordPerSystemPerCommit(t *testing.T) {
	ctx := context.Background()
	commits := []replay.CommitMutation{
		{
			Repo:   "example",
			Commit: "c1",
			Index:  0,
			Mutations: []replay.FileMutation{
				{Kind: replay.MutationAdd, Path: "README.md", Size: 5},
			},
			LiveStats: replay.LiveStats{FileCount: 1, LivePayloadBytes: 5},
			LiveFiles: []replay.LiveFile{{Path: "README.md", Size: 5}},
		},
		{
			Repo:   "example",
			Commit: "c2",
			Parent: "c1",
			Index:  1,
			Mutations: []replay.FileMutation{
				{Kind: replay.MutationModify, Path: "README.md", Size: 7},
			},
			LiveStats: replay.LiveStats{FileCount: 1, LivePayloadBytes: 7},
			LiveFiles: []replay.LiveFile{{Path: "README.md", Size: 7}},
		},
	}

	var out bytes.Buffer
	err := replay.RunJSONL(ctx, commits, []replay.SystemAdapter{
		&fakeAdapter{name: "maltflat"},
		&fakeAdapter{name: "merkledag"},
	}, &out)
	if err != nil {
		t.Fatalf("RunJSONL: %v", err)
	}

	dec := json.NewDecoder(&out)
	var records []replay.ResultRecord
	for dec.More() {
		var rec replay.ResultRecord
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("Decode record: %v", err)
		}
		records = append(records, rec)
	}

	if len(records) != 4 {
		t.Fatalf("record count = %d, want 4", len(records))
	}
	if records[0].System != "maltflat" || records[1].System != "merkledag" {
		t.Fatalf("systems for first commit = %q/%q, want maltflat/merkledag", records[0].System, records[1].System)
	}
	if records[2].Commit != "c2" || records[2].LiveStats.LivePayloadBytes != 7 {
		t.Fatalf("second commit record = commit %q live bytes %d, want c2/7", records[2].Commit, records[2].LiveStats.LivePayloadBytes)
	}
}
