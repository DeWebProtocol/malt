package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"
)

func TestReplayProtocolVerifiesModesAndRetainsNoOpCommits(t *testing.T) {
	data := []byte("file")
	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	mode, newMode, raw := uint32(0o644), uint32(0o755), true
	layout := rq3baseline.LayoutSpec{Model: "unixfs", FileLayout: "balanced", DirectoryLayout: "hamt", Chunking: rq3baseline.ChunkingSpec{Algorithm: "fixed", SizeBytes: 262144}, HAMTFanout: 256, RawFileLeaf: &raw}
	initial := rq3baseline.RunSpec{System: rq3baseline.SystemHAMTUnixFS, Layout: layout, Snapshot: rq3baseline.Snapshot{CommitID: "initial", Files: []rq3baseline.FrozenFile{{Path: "nested/file", FileKind: "regular", PayloadBase64: base64.StdEncoding.EncodeToString(data), PayloadSHA256: digest, Mode: &mode}}}, Commits: []rq3baseline.Commit{}}
	chunk := rq3baseline.RunSpec{System: rq3baseline.SystemHAMTUnixFS, Layout: layout, Commits: []rq3baseline.Commit{{CommitID: "mode", Mutations: []rq3baseline.Mutation{{Kind: "mode-change", Path: "nested/file", FileKind: "regular", ExpectedOldSHA256: digest, ExpectedOldMode: &mode, Mode: &newMode}}}, {CommitID: "no-op", Mutations: []rq3baseline.Mutation{}}}}
	var input, output bytes.Buffer
	encoder := json.NewEncoder(&input)
	for _, req := range []request{{Schema: requestSchema, ID: "1", Operation: "capabilities"}, {Schema: requestSchema, ID: "2", Operation: "start", Profile: "hamt-flat", Run: &initial}, {Schema: requestSchema, ID: "3", Operation: "chunk", Profile: "hamt-flat", Run: &chunk}, {Schema: requestSchema, ID: "4", Operation: "finish", Profile: "hamt-flat"}} {
		if err := encoder.Encode(req); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(context.Background(), &input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var replies [4]response
	for i := range replies {
		if err := decoder.Decode(&replies[i]); err != nil {
			t.Fatal(err)
		}
		if !replies[i].OK {
			t.Fatal(replies[i].Error)
		}
	}
	if !replies[3].Complete || !replies[3].ReadbackVerified || replies[3].Applied != 3 || replies[3].RetainedObjects == 0 {
		t.Fatal("incomplete verified history")
	}
	if replies[1].Records[0].Root == replies[2].Records[0].Root || replies[2].Records[0].Root != replies[2].Records[1].Root {
		t.Fatal("mode change or no-op root semantics lost")
	}
	if replies[2].Records[0].AdapterPayloadInputBytes != 0 || len(replies[2].Records[1].CAS.Events) != 0 || len(replies[2].ObjectRoles) != 2 {
		t.Fatal("zero-source commit or ownership accounting lost")
	}
}

func TestReplayRejectsAmbiguousOrIncompleteInput(t *testing.T) {
	for _, input := range []string{
		`{"schema_version":"malt-replay-worker-request/v1","request_id":"1","operation":"capabilities","profile":""} {}`,
		`{"schema_version":"malt-replay-worker-request/v1","request_id":"1","request_id":"2","operation":"capabilities","profile":""}`,
		`{"schema_version":"malt-replay-worker-request/v1","request_id":"1","operation":"capabilities","profile":""}`,
	} {
		var output bytes.Buffer
		if err := run(context.Background(), strings.NewReader(input+"\n"), &output); err == nil {
			t.Fatal("ambiguous or unfinished stream accepted")
		}
	}
}

func TestReplayEmptyInitialTreeDeleteAllAndResume(t *testing.T) {
	for _, profile := range []string{"merkledag-nested", "hamt-nested", "hamt-flat"} {
		t.Run(profile, func(t *testing.T) {
			raw, mode := true, uint32(0o644)
			layout := rq3baseline.LayoutSpec{Model: "unixfs", FileLayout: "balanced", DirectoryLayout: "hamt", Chunking: rq3baseline.ChunkingSpec{Algorithm: "fixed", SizeBytes: 262144}, HAMTFanout: 256, RawFileLeaf: &raw}
			system := rq3baseline.SystemHAMTUnixFS
			if profile == "merkledag-nested" {
				system, layout.DirectoryLayout, layout.HAMTFanout = rq3baseline.SystemMerkleDAGUnixFS, "basic", 0
			}
			initial := rq3baseline.RunSpec{System: system, Layout: layout, Snapshot: rq3baseline.Snapshot{CommitID: "empty", Files: []rq3baseline.FrozenFile{}}, Commits: []rq3baseline.Commit{}}
			data := []byte("from empty")
			digest := sha256.Sum256(data)
			hash := hex.EncodeToString(digest[:])
			put := rq3baseline.Mutation{Kind: "insert", Path: "nested/file", FileKind: "regular", Mode: &mode, PayloadBase64: base64.StdEncoding.EncodeToString(data), PayloadSHA256: hash}
			del := rq3baseline.Mutation{Kind: "delete", Path: "nested/file", FileKind: "regular", ExpectedOldMode: &mode, ExpectedOldSHA256: hash}
			chunk := rq3baseline.RunSpec{System: system, Layout: layout, Commits: []rq3baseline.Commit{{CommitID: "put", Mutations: []rq3baseline.Mutation{put}}, {CommitID: "delete", Mutations: []rq3baseline.Mutation{del}}, {CommitID: "no-op", Mutations: []rq3baseline.Mutation{}}, {CommitID: "resume", Mutations: []rq3baseline.Mutation{put}}, {CommitID: "empty-again", Mutations: []rq3baseline.Mutation{del}}}}
			var input, output bytes.Buffer
			encoder := json.NewEncoder(&input)
			for _, req := range []request{{Schema: requestSchema, ID: "1", Operation: "capabilities"}, {Schema: requestSchema, ID: "2", Operation: "start", Profile: profile, Run: &initial}, {Schema: requestSchema, ID: "3", Operation: "chunk", Profile: profile, Run: &chunk}, {Schema: requestSchema, ID: "4", Operation: "finish", Profile: profile}} {
				if err := encoder.Encode(req); err != nil {
					t.Fatal(err)
				}
			}
			if err := run(context.Background(), &input, &output); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(&output)
			var replies [4]response
			for i := range replies {
				if err := decoder.Decode(&replies[i]); err != nil {
					t.Fatal(err)
				}
			}
			if !replies[3].Complete || !replies[3].ReadbackVerified || replies[3].Applied != 6 {
				t.Fatal("empty history lost coverage or readback")
			}
			start := replies[1].Records[0]
			if start.AdapterPayloadInputBytes != 0 || start.Root == "" || start.CAS.Total.NewlyPersistedBytes == 0 {
				t.Fatal("empty root not materialized and accounted")
			}
			for _, index := range []int{1, 2, 4} {
				if replies[2].Records[index].Root != start.Root {
					t.Fatal("canonical empty root was not restored")
				}
			}
			if len(replies[2].Records[2].CAS.Events) != 0 {
				t.Fatal("empty no-op rewrote objects")
			}
		})
	}
}
