package rq3baseline

import (
	"context"
	"testing"
)

func TestFlatAndNestedHAMTUseDifferentPathIndexes(t *testing.T) {
	ctx := context.Background()
	mode, raw := uint32(0o644), true
	file := frozenFile
	spec := RunSpec{System: SystemHAMTUnixFS, Layout: LayoutSpec{Model: "unixfs", FileLayout: "balanced", DirectoryLayout: "hamt", Chunking: ChunkingSpec{Algorithm: "fixed", SizeBytes: 4}, HAMTFanout: 256, RawFileLeaf: &raw}, Snapshot: Snapshot{CommitID: "initial", Files: []FrozenFile{file("a/b.txt", []byte("one")), file("c.txt", []byte("two"))}}, Commits: []Commit{}}
	nested, n, err := StartStream(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := nested.Close(); err != nil {
			t.Error(err)
		}
	})
	flat, f, err := StartFlatHAMTStream(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := flat.Close(); err != nil {
			t.Error(err)
		}
	})
	if n.Root == f.Root {
		t.Fatal("flat and nested HAMT produced the same root")
	}
	for _, session := range []*StreamSession{nested, flat} {
		if err := session.VerifyPaths(ctx, []string{"a/b.txt", "c.txt", "missing.txt"}); err != nil {
			t.Fatal(err)
		}
		commit := Commit{CommitID: "delete", Mutations: []Mutation{{Kind: MutationDelete, FileKind: FileKindRegular, Path: "a/b.txt", ExpectedOldSHA256: digest([]byte("one")), ExpectedOldMode: &mode}}}
		if _, err := session.ApplyChunkWithReadback(ctx, []Commit{commit}); err != nil {
			t.Fatal(err)
		}
		if err := session.VerifyPaths(ctx, []string{"a/b.txt", "c.txt"}); err != nil {
			t.Fatal(err)
		}
		if err := session.VerifyAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var sum int64
	for _, event := range f.CAS.Events {
		if event.ObjectRole == "" {
			t.Fatal("CAS event lacks object ownership")
		}
		if event.Bytes != event.PayloadBytes+event.StructuralMetadataBytes {
			t.Fatal("byte components do not partition object")
		}
		if event.Status == statusNewlyPersisted {
			sum += event.Bytes
		}
	}
	if sum != f.CAS.Total.NewlyPersistedBytes {
		t.Fatal("event ledger does not reconcile")
	}
}

func TestReplayPreservesPermissionsForRawLeafFiles(t *testing.T) {
	ctx := context.Background()
	for _, flat := range []bool{false, true} {
		spec := baseSpec(SystemHAMTUnixFS, "hamt", 256, []FrozenFile{frozenFile("a/b", []byte("one"))})
		session, initial, err := StartReplayStream(ctx, spec, flat)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := session.Close(); err != nil {
				t.Error(err)
			}
		})
		if err := session.VerifyPaths(ctx, []string{"a/b"}); err != nil {
			t.Fatal(err)
		}
		mode := uint32(0o755)
		oldMode := uint32(0o644)
		mutation := Mutation{Kind: MutationModeChange, Path: "a/b", FileKind: FileKindRegular, ExpectedOldSHA256: digest([]byte("one")), ExpectedOldMode: &oldMode, Mode: &mode}
		records, err := session.ApplyChunkWithReadback(ctx, []Commit{{CommitID: "chmod", Mutations: []Mutation{mutation}}})
		if err != nil {
			t.Fatal(err)
		}
		if records[0].Root == initial.Root || records[0].AdapterPayloadInputBytes != 0 || records[0].CAS.Total.NewlyPersistedBytes == 0 {
			t.Fatal("mode-only update lost authenticated metadata or source boundary")
		}
	}
}
