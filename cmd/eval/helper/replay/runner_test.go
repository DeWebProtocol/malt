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
		Root:                    a.name + "-" + commit.Commit,
		AppliedMutations:        len(commit.Mutations),
		Accounting:              evalstore.NewMeter().Snapshot(),
		MaterializedPaths:       len(commit.LiveFiles),
		MaterializationStrategy: "fake-live-snapshot",
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

func TestRunCommitRecordsEmitsStructuredRecords(t *testing.T) {
	ctx := context.Background()
	commit := replay.CommitMutation{
		Repo:   "example",
		Commit: "c1",
		Index:  0,
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationAdd, Path: "README.md", Size: 5},
		},
		LiveStats: replay.LiveStats{FileCount: 1, LivePayloadBytes: 5},
		LiveFiles: []replay.LiveFile{{Path: "README.md", Size: 5}},
	}

	var records []replay.ResultRecord
	err := replay.RunCommitRecords(ctx, commit, []replay.SystemAdapter{
		&fakeAdapter{name: "maltflat"},
	}, func(record replay.ResultRecord) error {
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatalf("RunCommitRecords: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Repo != "example" || record.System != "maltflat" || record.Commit != "c1" || record.Index != 0 {
		t.Fatalf("record identity = %+v, want repo/system/commit/index preserved", record)
	}
	if len(record.MutationSet) != 1 || record.MutationSet[0].Path != "README.md" {
		t.Fatalf("mutations = %+v, want README.md add preserved", record.MutationSet)
	}
	if record.LiveStats.LivePayloadBytes != 5 {
		t.Fatalf("live stats = %+v, want payload bytes preserved", record.LiveStats)
	}
	if record.Result.MaterializationStrategy != "fake-live-snapshot" {
		t.Fatalf("materialization strategy = %q, want fake-live-snapshot", record.Result.MaterializationStrategy)
	}
	if record.Accounting.Total.NewObjectCount != record.Result.Accounting.Total.NewObjectCount {
		t.Fatalf("top-level accounting = %+v, want result accounting %+v", record.Accounting, record.Result.Accounting)
	}
}

type accountingAdapter struct {
	name string
}

func (a accountingAdapter) Name() string {
	return a.name
}

func (a accountingAdapter) Apply(context.Context, replay.CommitMutation) (replay.ApplyResult, error) {
	accounting := evalstore.Snapshot{
		Total: evalstore.Counter{NewPersistedBytes: 30},
		Categories: map[evalstore.Category]evalstore.Counter{
			evalstore.CategoryCASPayload: {NewPersistedBytes: 10},
			evalstore.CategoryArcTable:   {NewPersistedBytes: 20},
		},
	}
	return replay.ApplyResult{
		Root:             a.name + "-root",
		AppliedMutations: 1,
		Accounting:       accounting,
		AccountingDelta:  accounting,
	}, nil
}

func TestRunCommitRecordsReportsWriteAmplificationInputs(t *testing.T) {
	ctx := context.Background()
	commit := replay.CommitMutation{
		Repo:   "example",
		Commit: "c1",
		Index:  0,
		Mutations: []replay.FileMutation{
			{Kind: replay.MutationModify, Path: "a.txt", Size: 10},
			{Kind: replay.MutationRename, OldPath: "b.txt", Path: "c.txt", Size: 99},
			{Kind: replay.MutationRename, OldPath: "edited-old.txt", Path: "edited-new.txt", Size: 20, ContentChanged: true},
			{Kind: replay.MutationDelete, Path: "d.txt"},
		},
	}

	var records []replay.ResultRecord
	err := replay.RunCommitRecords(ctx, commit, []replay.SystemAdapter{
		accountingAdapter{name: "maltflat"},
	}, func(record replay.ResultRecord) error {
		records = append(records, record)
		return nil
	})
	if err != nil {
		t.Fatalf("RunCommitRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.LogicalChangedPayloadBytes != 30 {
		t.Fatalf("logical changed payload bytes = %d, want modify plus edited rename bytes", record.LogicalChangedPayloadBytes)
	}
	if record.PhysicalPersistedBytes != 30 || record.PhysicalPayloadBytes != 10 || record.PhysicalMetadataBytes != 20 {
		t.Fatalf("physical byte split = total %d payload %d metadata %d, want 30/10/20",
			record.PhysicalPersistedBytes, record.PhysicalPayloadBytes, record.PhysicalMetadataBytes)
	}
	if record.WriteAmplification == nil || *record.WriteAmplification != 1 {
		t.Fatalf("write amplification = %v, want 1", record.WriteAmplification)
	}
}
