package rq3baseline

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	merkledagimport "github.com/dewebprotocol/malt-client/merkledag/importer"
	cid "github.com/ipfs/go-cid"
)

func TestCleanupFailedPayloadFileReturnsCloseAndRemoveFailures(t *testing.T) {
	primary := errors.New("write failed")
	closeErr := errors.New("close failed")
	removeErr := errors.New("remove failed")
	got := cleanupFailedPayloadFile("payload", primary, closeErr, func(path string) error {
		if path != "payload" {
			t.Fatalf("remove path = %q", path)
		}
		return removeErr
	})
	for _, want := range []error{primary, closeErr, removeErr} {
		if !errors.Is(got, want) {
			t.Fatalf("cleanup error %v does not include %v", got, want)
		}
	}
	if got := cleanupFailedPayloadFile("payload", primary, nil, func(string) error { return os.ErrNotExist }); !errors.Is(got, primary) || errors.Is(got, os.ErrNotExist) {
		t.Fatalf("not-exist cleanup should retain only the primary failure: %v", got)
	}
}

func TestPayloadStoreReconcileRetainsOnlyLiveCheckout(t *testing.T) {
	store, err := newPayloadStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.close(); err != nil {
			t.Errorf("close payload store: %v", err)
		}
	})
	oldData, liveData := []byte("historical"), []byte("live")
	oldSum, liveSum := sha256.Sum256(oldData), sha256.Sum256(liveData)
	oldDigest, liveDigest := hex.EncodeToString(oldSum[:]), hex.EncodeToString(liveSum[:])
	if err := store.put(oldDigest, oldData); err != nil {
		t.Fatal(err)
	}
	if err := store.put(liveDigest, liveData); err != nil {
		t.Fatal(err)
	}
	live := map[string]logicalFile{"file": {hash: liveDigest, size: len(liveData)}}
	if err := store.reconcile(live); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read(logicalFile{hash: oldDigest, size: len(oldData)}); !os.IsNotExist(err) {
		t.Fatalf("historical payload read error = %v, want not-exist", err)
	}
	if got, err := store.read(live["file"]); err != nil || string(got) != string(liveData) {
		t.Fatalf("live payload = %q, %v", got, err)
	}
}

func TestRunBasicUnixFSEmitsRootPerCommitAndExactDedupAccounting(t *testing.T) {
	old := []byte("same-data")
	appended := []byte("same-data-more")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{
		frozenFile("docs/a.txt", old),
		frozenFile("docs/b.txt", old),
	})
	spec.Commits = []Commit{
		{CommitID: "c2-same-value", Mutations: []Mutation{replaceMutation("docs/a.txt", old, old)}},
		{CommitID: "c3-append", Mutations: []Mutation{appendMutation("docs/a.txt", old, appended)}},
		{CommitID: "c4-rename", Mutations: []Mutation{pathMutation(MutationRename, "docs/b.txt", "docs/c.txt", old)}},
		{CommitID: "c5-move", Mutations: []Mutation{pathMutation(MutationMove, "docs/c.txt", "archive/c.txt", old)}},
		{CommitID: "c6-delete", Mutations: []Mutation{pathMutation(MutationDelete, "archive/c.txt", "", old)}},
	}

	result, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1+len(spec.Commits) {
		t.Fatalf("records = %d, want %d", len(result.Records), 1+len(spec.Commits))
	}
	if result.WorkloadSHA256 == "" || result.CapabilityID != Capability().CapabilityID {
		t.Fatalf("missing provenance: %#v", result)
	}
	for i, record := range result.Records {
		if _, err := cid.Parse(record.Root); err != nil {
			t.Fatalf("record[%d] root %q: %v", i, record.Root, err)
		}
		if i > 0 && record.ParentRoot != result.Records[i-1].Root {
			t.Fatalf("record[%d] parent = %q, want %q", i, record.ParentRoot, result.Records[i-1].Root)
		}
		if record.ClientPhases.ClientComputeWallNanos <= 0 {
			t.Fatalf("record[%d] omitted client compute phase: %#v", i, record.ClientPhases)
		}
		assertAccountingAddsUp(t, record.CAS)
	}

	snapshot := result.Records[0]
	if got, want := snapshot.AdapterPayloadInputBytes, int64(len(old)*2); got != want {
		t.Fatalf("snapshot adapter input bytes = %d, want full source snapshot %d", got, want)
	}
	if snapshot.CAS.PayloadChunks.NewlyPersistedBytes == 0 || snapshot.CAS.StructuralMetadata.NewlyPersistedBytes == 0 {
		t.Fatalf("snapshot did not separate payload and structure: %#v", snapshot.CAS)
	}
	if snapshot.CAS.PayloadChunks.DuplicateObjects == 0 {
		t.Fatalf("identical payload chunks were not identified as same-commit duplicates: %#v", snapshot.CAS.PayloadChunks)
	}

	sameValue := result.Records[1]
	if got, want := sameValue.AdapterPayloadInputBytes, int64(len(old)); got != want {
		t.Fatalf("same-value replace adapter input bytes = %d, want full post-image %d", got, want)
	}
	if sameValue.Root != sameValue.ParentRoot {
		t.Fatalf("same-value replace changed content-addressed root: parent=%s root=%s", sameValue.ParentRoot, sameValue.Root)
	}
	if sameValue.CAS.Total.NewlyPersistedObjects != 0 || sameValue.CAS.Total.AlreadyPresentObjects == 0 {
		t.Fatalf("same-value replace accounting = %#v", sameValue.CAS.Total)
	}

	appendRecord := result.Records[2]
	if got, want := appendRecord.LogicalPayloadBytes, int64(len(appended)-len(old)); got != want {
		t.Fatalf("append logical bytes = %d, want %d", got, want)
	}
	if appendRecord.CAS.Total.NewlyPersistedObjects == 0 {
		t.Fatal("append persisted no new CAS objects")
	}
	if got, want := appendRecord.AdapterPayloadInputBytes, int64(len(appended)); got != want {
		t.Fatalf("append adapter input bytes = %d, want full post-image %d", got, want)
	}

	renameRecord := result.Records[3]
	if renameRecord.LogicalPayloadBytes != 0 || renameRecord.LogicalBindingsChanged != 2 || renameRecord.Mutations[0].Translation != "remove_file_then_put_file" {
		t.Fatalf("rename semantics = %#v", renameRecord)
	}
	if renameRecord.AdapterPayloadInputBytes != 0 || result.Records[4].AdapterPayloadInputBytes != 0 || result.Records[5].AdapterPayloadInputBytes != 0 {
		t.Fatalf("path-only/delete adapter input bytes must be zero: rename=%d move=%d delete=%d", renameRecord.AdapterPayloadInputBytes, result.Records[4].AdapterPayloadInputBytes, result.Records[5].AdapterPayloadInputBytes)
	}
	if renameRecord.CAS.Total.AttemptedObjects == 0 {
		t.Fatal("rename did not include intermediate Editor writes")
	}

	second, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkloadSHA256 != result.WorkloadSHA256 {
		t.Fatalf("workload digest changed: %s != %s", second.WorkloadSHA256, result.WorkloadSHA256)
	}
	for i := range result.Records {
		if result.Records[i].Root != second.Records[i].Root || !reflect.DeepEqual(result.Records[i].CAS, second.Records[i].CAS) {
			t.Fatalf("deterministic result mismatch at record %d", i)
		}
	}
}

func TestStreamSessionMatchesSingleRunAcrossChunks(t *testing.T) {
	payload := []byte("same-data")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{
		frozenFile("docs/a.txt", payload),
		frozenFile("docs/b.txt", payload),
	})
	spec.Commits = []Commit{
		{CommitID: "c2-replace", Mutations: []Mutation{replaceMutation("docs/a.txt", payload, []byte("next"))}},
		{CommitID: "c3-rename", Mutations: []Mutation{pathMutation(MutationRename, "docs/b.txt", "docs/c.txt", payload)}},
		{CommitID: "c4-delete", Mutations: []Mutation{pathMutation(MutationDelete, "docs/c.txt", "", payload)}},
	}

	want, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	streamSpec := spec
	streamSpec.Commits = []Commit{}
	session, snapshot, err := StartStream(t.Context(), streamSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close stream session: %v", err)
		}
	})
	for _, path := range []string{"docs/a.txt", "docs/b.txt"} {
		if session.state[path].data != nil || session.validationState[path].data != nil {
			t.Fatalf("stream retained %q payload bytes in live heap state", path)
		}
		stored, err := session.payloads.read(session.state[path])
		if err != nil || !reflect.DeepEqual(stored, payload) {
			t.Fatalf("stream disk payload %q = %q, %v", path, stored, err)
		}
	}
	first, err := session.ApplyChunk(t.Context(), spec.Commits[:1])
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.ApplyChunk(t.Context(), spec.Commits[1:])
	if err != nil {
		t.Fatal(err)
	}
	got := append([]CommitRecord{snapshot}, first...)
	got = append(got, second...)
	if len(got) != len(want.Records) {
		t.Fatalf("stream records = %d, want %d", len(got), len(want.Records))
	}
	for index := range got {
		got[index].ClientPhases = ClientPhases{}
		want.Records[index].ClientPhases = ClientPhases{}
		if !reflect.DeepEqual(got[index], want.Records[index]) {
			t.Fatalf("stream record[%d] differs from single run:\n got: %#v\nwant: %#v", index, got[index], want.Records[index])
		}
	}
	if session.Root() != got[len(got)-1].Root {
		t.Fatalf("stream root = %q, want final record root %q", session.Root(), got[len(got)-1].Root)
	}
}

func TestStreamSessionRejectsDuplicateCommitAcrossChunksWithoutAdvancing(t *testing.T) {
	payload := []byte("payload")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("file", payload)})
	session, _, err := StartStream(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	commit := Commit{CommitID: "c2", Mutations: []Mutation{replaceMutation("file", payload, []byte("next"))}}
	if _, err := session.ApplyChunk(t.Context(), []Commit{commit}); err != nil {
		t.Fatal(err)
	}
	root := session.Root()
	if _, err := session.ApplyChunk(t.Context(), []Commit{commit}); err == nil || !strings.Contains(err.Error(), "duplicate commit_id") {
		t.Fatalf("duplicate commit error = %v", err)
	}
	if session.Root() != root {
		t.Fatalf("duplicate commit advanced root from %q to %q", root, session.Root())
	}
}

func TestOneShotAppendDirectlyAfterSnapshotCountsOnlySuffix(t *testing.T) {
	old := []byte("base")
	next := []byte("base-suffix")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("file", old)})
	spec.Commits = []Commit{{CommitID: "append", Mutations: []Mutation{appendMutation("file", old, next)}}}
	result, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Records[1].LogicalPayloadBytes, int64(len(next)-len(old)); got != want {
		t.Fatalf("direct snapshot append logical bytes = %d, want suffix %d", got, want)
	}
}

func TestModeChangePreservesDiskBackedSizeAcrossAppendAndRename(t *testing.T) {
	old := []byte("base")
	next := []byte("base-suffix")
	oldMode, newMode := uint32(0o644), uint32(0o755)
	appendAfterMode := appendMutation("file", old, next)
	appendAfterMode.ExpectedOldMode, appendAfterMode.Mode = &newMode, &newMode
	renameAfterMode := pathMutation(MutationRename, "file", "renamed", next)
	renameAfterMode.ExpectedOldMode, renameAfterMode.Mode = &newMode, &newMode
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("file", old)})
	spec.Commits = []Commit{
		{CommitID: "chmod", Mutations: []Mutation{{Kind: MutationModeChange, Path: "file", FileKind: FileKindRegular, ExpectedOldSHA256: digest(old), ExpectedOldMode: &oldMode, Mode: &newMode}}},
		{CommitID: "append", Mutations: []Mutation{appendAfterMode}},
		{CommitID: "rename", Mutations: []Mutation{renameAfterMode}},
	}
	oneShot, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := oneShot.Records[2].LogicalPayloadBytes, int64(len(next)-len(old)); got != want {
		t.Fatalf("one-shot append after mode-change logical bytes = %d, want %d", got, want)
	}

	streamSpec := spec
	streamSpec.Commits = []Commit{}
	session, _, err := StartStream(t.Context(), streamSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close mode-change stream: %v", err)
		}
	})
	for index, commit := range spec.Commits {
		records, err := session.ApplyChunk(t.Context(), []Commit{commit})
		if err != nil {
			t.Fatalf("stream commit %d (%s): %v", index, commit.CommitID, err)
		}
		if commit.CommitID == "append" && records[0].LogicalPayloadBytes != int64(len(next)-len(old)) {
			t.Fatalf("stream append after mode-change logical bytes = %d", records[0].LogicalPayloadBytes)
		}
	}
}

func TestValidateAndAccountSourceDoesNotConflateAdapterInputWithLogicalDelta(t *testing.T) {
	old := []byte("abcd")
	next := []byte("abcdefgh")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("file", old)})
	spec.Commits = []Commit{
		{CommitID: "append", Mutations: []Mutation{appendMutation("file", old, next)}},
		{CommitID: "rename", Mutations: []Mutation{pathMutation(MutationRename, "file", "renamed", next)}},
	}
	accounting, err := ValidateAndAccountSource(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounting) != 3 || accounting[0].AdapterPayloadInputBytes != int64(len(old)) ||
		accounting[1].AdapterPayloadInputBytes != int64(len(next)) || accounting[2].AdapterPayloadInputBytes != 0 ||
		accounting[2].LogicalBindingsChanged != 2 {
		t.Fatalf("source accounting = %#v", accounting)
	}
}

func TestRunHAMTUsesCurrentEditorAndAccountsMixedBatch(t *testing.T) {
	files := make([]FrozenFile, 0, 32)
	for i := 0; i < 32; i++ {
		files = append(files, frozenFile(fmt.Sprintf("files/%02d.txt", i), []byte(fmt.Sprintf("payload-%02d", i))))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	spec := baseSpec(SystemHAMTUnixFS, "hamt", 16, files)
	spec.Commits = []Commit{{CommitID: "hamt-mixed", Mutations: []Mutation{
		insertMutation("files/zz.txt", []byte("new")),
		pathMutation(MutationDelete, "files/00.txt", "", []byte("payload-00")),
	}}}

	result, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || result.Records[1].Root == result.Records[0].Root {
		t.Fatalf("HAMT roots = %#v", result.Records)
	}
	commit := result.Records[1]
	if commit.LogicalObjectsChanged != 2 || commit.LogicalBindingsChanged != 2 {
		t.Fatalf("HAMT mixed logical accounting = %#v", commit)
	}
	if commit.CAS.StructuralMetadata.AttemptedObjects == 0 || commit.CAS.Reads.Objects == 0 {
		t.Fatalf("HAMT path did not expose structural writes/reads: %#v", commit.CAS)
	}
	for _, record := range result.Records {
		for _, event := range record.CAS.Events {
			if event.Category == categoryMixed {
				t.Fatalf("raw-leaf HAMT bitfield was misclassified as payload: %#v", event)
			}
		}
	}
}

func TestRunSupportsCurrentTrickleFileLayout(t *testing.T) {
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("file.txt", []byte("0123456789abcdef"))})
	spec.Layout.FileLayout = "trickle"
	result, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 1 || result.Records[0].CAS.PayloadChunks.NewlyPersistedObjects < 2 {
		t.Fatalf("trickle result = %#v", result)
	}
}

func TestRunNonRawLeavesDecodeExactMixedByteAccounting(t *testing.T) {
	payload := []byte("embedded-payload")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("file.txt", payload)})
	raw := false
	spec.Layout.RawFileLeaf = &raw
	result, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	accounting := result.Records[0].CAS
	foundMixed := false
	for _, event := range accounting.Events {
		if event.PayloadBytes+event.StructuralMetadataBytes != event.Bytes {
			t.Fatalf("event byte split is not exact: %#v", event)
		}
		if event.Category == categoryMixed {
			foundMixed = true
		}
	}
	if !foundMixed || accounting.PayloadChunks.NewlyPersistedBytes == 0 || accounting.StructuralMetadata.NewlyPersistedBytes == 0 {
		t.Fatalf("non-raw accounting = %#v", accounting)
	}
	imported, err := merkledagimport.ImportFiles(t.Context(), casmemory.New(), []merkledagimport.File{{Path: "file.txt", Data: payload, Mode: 0o644}}, merkledagimport.Options{
		Model: merkledagimport.ModelUnixFS, FileLayout: merkledagimport.FileLayoutBalanced, DirLayout: merkledagimport.DirLayoutBasic, ChunkSize: 4, RawFileLeaf: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Records[0].Root != imported.Root {
		t.Fatalf("Editor snapshot root %s differs from current ImportFiles root %s", result.Records[0].Root, imported.Root)
	}
}

func TestRunModeChangeMovesOnlyStructuralMetadata(t *testing.T) {
	payload := []byte("unchanged")
	spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("script", payload)})
	oldMode, newMode := uint32(0o644), uint32(0o755)
	spec.Commits = []Commit{{CommitID: "chmod", Mutations: []Mutation{{
		Kind: MutationModeChange, Path: "script", FileKind: FileKindRegular, ExpectedOldSHA256: digest(payload), ExpectedOldMode: &oldMode, Mode: &newMode,
	}}}}
	result, err := Run(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	record := result.Records[1]
	if record.Root == record.ParentRoot || record.LogicalPayloadBytes != 0 || record.Mutations[0].Translation != "put_file_mode_change" {
		t.Fatalf("mode-change record = %#v", record)
	}
	if record.CAS.PayloadChunks.NewlyPersistedObjects != 0 || record.CAS.StructuralMetadata.NewlyPersistedObjects == 0 {
		t.Fatalf("mode-change byte placement = %#v", record.CAS)
	}
}

func TestRunFailsClosedForCapabilityAndFrozenWorkloadViolations(t *testing.T) {
	payload := []byte("payload")
	tests := []struct {
		name    string
		mutate  func(*RunSpec)
		wantGap string
		wantErr string
	}{
		{
			name: "invalid HAMT fanout",
			mutate: func(spec *RunSpec) {
				spec.System = SystemHAMTUnixFS
				spec.Layout.DirectoryLayout = "hamt"
				spec.Layout.HAMTFanout = 2
			},
			wantErr: "multiple of 8",
		},
		{
			name: "directory rename",
			mutate: func(spec *RunSpec) {
				spec.Commits = []Commit{{CommitID: "c2", Mutations: []Mutation{pathMutation(MutationRename, "docs", "archive", payload)}}}
			},
			wantGap: "directory_subtree_mutation",
		},
		{
			name: "symlink snapshot",
			mutate: func(spec *RunSpec) {
				spec.Snapshot.Files[0].FileKind = "symlink"
			},
			wantGap: "symlink_and_special_file_mutation",
		},
		{
			name: "digest mismatch",
			mutate: func(spec *RunSpec) {
				spec.Snapshot.Files[0].PayloadSHA256 = strings.Repeat("0", 64)
			},
			wantErr: "payload SHA-256 mismatch",
		},
		{
			name: "noncanonical base64",
			mutate: func(spec *RunSpec) {
				spec.Snapshot.Files[0].PayloadBase64 = base64.RawStdEncoding.EncodeToString(payload)
			},
			wantErr: "not canonical standard base64",
		},
		{
			name: "unsorted snapshot",
			mutate: func(spec *RunSpec) {
				spec.Snapshot.Files = []FrozenFile{frozenFile("z", payload), frozenFile("a", payload)}
			},
			wantErr: "strictly sorted",
		},
		{
			name: "stale old digest",
			mutate: func(spec *RunSpec) {
				mutation := replaceMutation("docs/file.txt", []byte("stale"), []byte("next"))
				spec.Commits = []Commit{{CommitID: "c2", Mutations: []Mutation{mutation}}}
			},
			wantErr: "old payload digest mismatch",
		},
		{
			name: "append is not prefix extension",
			mutate: func(spec *RunSpec) {
				spec.Commits = []Commit{{CommitID: "c2", Mutations: []Mutation{appendMutation("docs/file.txt", payload, []byte("other"))}}}
			},
			wantErr: "strictly extend",
		},
		{
			name: "projected chunk output is bounded",
			mutate: func(spec *RunSpec) {
				spec.Layout.Chunking.SizeBytes = 1
				spec.Snapshot.Files = []FrozenFile{frozenFile("large", make([]byte, maxProjectedChunks+1))}
			},
			wantErr: "projected payload chunks exceed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := baseSpec(SystemMerkleDAGUnixFS, "basic", 0, []FrozenFile{frozenFile("docs/file.txt", payload)})
			test.mutate(&spec)
			_, err := Run(t.Context(), spec)
			if err == nil {
				t.Fatal("Run succeeded")
			}
			if test.wantGap != "" {
				unsupported, ok := err.(*UnsupportedError)
				if !ok || unsupported.Gap != test.wantGap {
					t.Fatalf("error = %#v, want gap %q", err, test.wantGap)
				}
			} else if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestCapabilityDocumentsFailClosedBoundaries(t *testing.T) {
	capability := Capability()
	if got, want := capability.SafetyLimits.MaximumJSONLRecordBytes, 512<<20; got != want {
		t.Fatalf("maximum JSONL record bytes = %d, want %d", got, want)
	}
	if got, want := capability.SafetyLimits.MaximumDecodedPayloadBytes, 256<<20; got != want {
		t.Fatalf("maximum decoded payload bytes = %d, want %d", got, want)
	}
	want := map[string]bool{
		"directory_subtree_mutation": false,
		"physical_backend_bytes":     false,
	}
	for _, gap := range capability.Gaps {
		if !gap.FailClosed {
			t.Errorf("capability gap %q is not fail closed", gap.Code)
		}
		if _, exists := want[gap.Code]; exists {
			want[gap.Code] = gap.FailClosed
		}
	}
	for code, failClosed := range want {
		if !failClosed {
			t.Errorf("capability does not declare fail-closed gap %q", code)
		}
	}
}

func baseSpec(system, directoryLayout string, fanout int, files []FrozenFile) RunSpec {
	raw := true
	return RunSpec{
		System: system,
		Layout: LayoutSpec{
			Model:           "unixfs",
			FileLayout:      "balanced",
			DirectoryLayout: directoryLayout,
			Chunking:        ChunkingSpec{Algorithm: "fixed", SizeBytes: 4},
			HAMTFanout:      fanout,
			RawFileLeaf:     &raw,
		},
		Snapshot: Snapshot{CommitID: "c1", Files: files},
		Commits:  []Commit{},
	}
}

func frozenFile(filePath string, data []byte) FrozenFile {
	mode := uint32(0o644)
	return FrozenFile{
		Path:          filePath,
		FileKind:      FileKindRegular,
		PayloadBase64: base64.StdEncoding.EncodeToString(data),
		PayloadSHA256: digest(data),
		Mode:          &mode,
	}
}

func insertMutation(filePath string, data []byte) Mutation {
	mode := uint32(0o644)
	return Mutation{Kind: MutationInsert, Path: filePath, FileKind: FileKindRegular, PayloadBase64: base64.StdEncoding.EncodeToString(data), PayloadSHA256: digest(data), Mode: &mode}
}

func replaceMutation(filePath string, old, next []byte) Mutation {
	mutation := insertMutation(filePath, next)
	mutation.Kind = MutationReplace
	mutation.ExpectedOldSHA256 = digest(old)
	expectedMode := uint32(0o644)
	mutation.ExpectedOldMode = &expectedMode
	return mutation
}

func appendMutation(filePath string, old, next []byte) Mutation {
	mutation := replaceMutation(filePath, old, next)
	mutation.Kind = MutationAppend
	return mutation
}

func pathMutation(kind, source, destination string, old []byte) Mutation {
	expectedMode := uint32(0o644)
	mutation := Mutation{Kind: kind, Path: source, FileKind: FileKindRegular, Destination: destination, ExpectedOldSHA256: digest(old), ExpectedOldMode: &expectedMode}
	if kind == MutationRename || kind == MutationMove {
		mode := uint32(0o644)
		mutation.Mode = &mode
	}
	return mutation
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func assertAccountingAddsUp(t *testing.T, accounting CASAccounting) {
	t.Helper()
	if accounting.Total.AttemptedObjects != len(accounting.Events) {
		t.Fatalf("attempted objects = %d, events = %d", accounting.Total.AttemptedObjects, len(accounting.Events))
	}
	if accounting.Total.AttemptedObjects != accounting.Total.NewlyPersistedObjects+accounting.Total.AlreadyPresentObjects+accounting.Total.DuplicateObjects {
		t.Fatalf("status counts do not partition attempts: %#v", accounting.Total)
	}
	if accounting.Total.AttemptedBytes != accounting.Total.NewlyPersistedBytes+accounting.Total.AlreadyPresentBytes+accounting.Total.DuplicateBytes {
		t.Fatalf("status bytes do not partition attempts: %#v", accounting.Total)
	}
	if accounting.Total.AttemptedBytes != accounting.PayloadChunks.AttemptedBytes+accounting.StructuralMetadata.AttemptedBytes ||
		accounting.Total.NewlyPersistedBytes != accounting.PayloadChunks.NewlyPersistedBytes+accounting.StructuralMetadata.NewlyPersistedBytes ||
		accounting.Total.AlreadyPresentBytes != accounting.PayloadChunks.AlreadyPresentBytes+accounting.StructuralMetadata.AlreadyPresentBytes ||
		accounting.Total.DuplicateBytes != accounting.PayloadChunks.DuplicateBytes+accounting.StructuralMetadata.DuplicateBytes {
		t.Fatalf("byte categories do not partition attempts and statuses: %#v", accounting)
	}
}
