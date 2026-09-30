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

func TestReplayReadbackDistinguishesFileAndDirectoryReplacements(t *testing.T) {
	for _, profile := range []string{"merkledag-nested", "hamt-nested", "hamt-flat"} {
		t.Run(profile, func(t *testing.T) {
			mode := uint32(0o644)
			system, directory, fanout := SystemHAMTUnixFS, "hamt", 256
			if profile == "merkledag-nested" {
				system, directory, fanout = SystemMerkleDAGUnixFS, "basic", 0
			}
			spec := baseSpec(system, directory, fanout, []FrozenFile{frozenFile("a", []byte("old-a")), frozenFile("b/c", []byte("old-c"))})
			session, _, err := StartReplayStream(t.Context(), spec, profile == "hamt-flat")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			insert := func(path, body string) Mutation {
				file := frozenFile(path, []byte(body))
				return Mutation{Kind: MutationInsert, FileKind: FileKindRegular, Path: path, PayloadBase64: file.PayloadBase64, PayloadSHA256: file.PayloadSHA256, Mode: &mode}
			}
			remove := func(path, body string) Mutation {
				return Mutation{Kind: MutationDelete, FileKind: FileKindRegular, Path: path, ExpectedOldSHA256: digest([]byte(body)), ExpectedOldMode: &mode}
			}
			commits := []Commit{
				{CommitID: "swap", Mutations: []Mutation{remove("a", "old-a"), insert("a/x", "new-x"), remove("b/c", "old-c"), insert("b", "new-b")}},
				{CommitID: "reverse", Mutations: []Mutation{remove("a/x", "new-x"), insert("a", "final-a"), remove("b", "new-b"), insert("b/c", "final-c")}},
			}
			for _, commit := range commits {
				if _, err := session.ApplyChunkWithReadback(t.Context(), []Commit{commit}); err != nil {
					t.Fatal(err)
				}
				if err := session.VerifyAll(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			// Actual directory bindings still cannot stand in for expected files.
			actual := session.state["b/c"]
			session.state["b"] = actual
			if err := session.VerifyPaths(t.Context(), []string{"b"}); err == nil {
				t.Fatal("a directory was accepted as a source regular file")
			}
			delete(session.state, "b")
			if err := session.VerifyPaths(t.Context(), []string{"a"}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
