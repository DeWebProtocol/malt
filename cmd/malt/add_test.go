package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/server"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestMountAddInputsWrapRules(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("a"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir dir: %v", err)
	}

	inputs, err := collectAddInputs([]string{file, dir})
	if err != nil {
		t.Fatalf("collect inputs: %v", err)
	}

	if _, err := mountAddInputs(inputs, addBuildOptions{Wrap: true}); err == nil {
		t.Fatal("expected error when multi-input wrap has no wrap-name")
	}
	if _, err := mountAddInputs(inputs[:1], addBuildOptions{Wrap: true, WrapName: "bundle"}); err != nil {
		t.Fatalf("single file wrap should pass: %v", err)
	}
}

func TestMountAddInputsPathModes(t *testing.T) {
	root := t.TempDir()
	fileA := filepath.Join(root, "a.txt")
	fileB := filepath.Join(root, "b.txt")
	if err := os.WriteFile(fileA, []byte("a"), 0o644); err != nil {
		t.Fatalf("write fileA: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("b"), 0o644); err != nil {
		t.Fatalf("write fileB: %v", err)
	}
	dir := filepath.Join(root, "docs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}

	inputs, err := collectAddInputs([]string{fileA, fileB})
	if err != nil {
		t.Fatalf("collect file inputs: %v", err)
	}
	singleMounted, err := mountAddInputs(inputs[:1], addBuildOptions{})
	if err != nil {
		t.Fatalf("mount single file: %v", err)
	}
	if singleMounted[0].MountBase != "a.txt" {
		t.Fatalf("single file mount = %q, want %q", singleMounted[0].MountBase, "a.txt")
	}
	mounted, err := mountAddInputs(inputs, addBuildOptions{Prefix: "/repo//", Wrap: true, WrapName: "bundle"})
	if err != nil {
		t.Fatalf("mount wrapped inputs: %v", err)
	}
	if mounted[0].MountBase != "repo/bundle/a.txt" {
		t.Fatalf("mounted[0] = %q", mounted[0].MountBase)
	}
	if mounted[1].MountBase != "repo/bundle/b.txt" {
		t.Fatalf("mounted[1] = %q", mounted[1].MountBase)
	}

	dirInputs, err := collectAddInputs([]string{dir})
	if err != nil {
		t.Fatalf("collect dir input: %v", err)
	}
	dirMounted, err := mountAddInputs(dirInputs, addBuildOptions{Prefix: "repo"})
	if err != nil {
		t.Fatalf("mount dir input: %v", err)
	}
	if dirMounted[0].MountBase != "repo/docs" {
		t.Fatalf("dir mount = %q, want %q", dirMounted[0].MountBase, "repo/docs")
	}
}

func TestAddUsesClientWriterMutationFacade(t *testing.T) {
	data, err := os.ReadFile("add.go")
	if err != nil {
		t.Fatalf("ReadFile(add.go): %v", err)
	}
	if strings.Contains(string(data), "httpapi.SemanticMutationRequest") {
		t.Fatal("add.go should use client writer mutation facade instead of constructing httpapi.SemanticMutationRequest")
	}
}

func TestNormalizeAddBuildOptions(t *testing.T) {
	tests := []struct {
		name           string
		in             addBuildOptions
		wantTarget     string
		wantModel      string
		wantLayout     string
		wantFileLayout string
		wantDirLayout  string
		wantErr        bool
	}{
		{
			name:       "defaults to malt unixfs flat",
			wantTarget: addTargetMALT,
			wantModel:  addModelUnixFS,
			wantLayout: addLayoutFlat,
		},
		{
			name: "malt hierarchical",
			in: addBuildOptions{
				Target: addTargetMALT,
				Model:  addModelUnixFS,
				Layout: addLayoutHierarchical,
			},
			wantTarget: addTargetMALT,
			wantModel:  addModelUnixFS,
			wantLayout: addLayoutHierarchical,
		},
		{
			name: "merkle dag defaults split file and dir layout",
			in: addBuildOptions{
				Target: addTargetMerkleDAG,
				Model:  addModelUnixFS,
			},
			wantTarget:     addTargetMerkleDAG,
			wantModel:      addModelUnixFS,
			wantLayout:     "",
			wantFileLayout: addFileLayoutBalanced,
			wantDirLayout:  addDirLayoutAdaptive,
		},
		{
			name: "rejects malt hamt",
			in: addBuildOptions{
				Target: addTargetMALT,
				Model:  addModelUnixFS,
				Layout: "hamt",
			},
			wantErr: true,
		},
		{
			name: "rejects merkle dag top-level layout",
			in: addBuildOptions{
				Target: addTargetMerkleDAG,
				Model:  addModelUnixFS,
				Layout: addLayoutFlat,
			},
			wantErr: true,
		},
		{
			name: "rejects unknown model",
			in: addBuildOptions{
				Target: addTargetMALT,
				Model:  "posix",
				Layout: addLayoutFlat,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeAddBuildOptions(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if got.Target != tt.wantTarget || got.Model != tt.wantModel || got.Layout != tt.wantLayout {
				t.Fatalf("got target/model/layout = %q/%q/%q", got.Target, got.Model, got.Layout)
			}
			if got.FileLayout != tt.wantFileLayout || got.DirLayout != tt.wantDirLayout {
				t.Fatalf("got file/dir layout = %q/%q", got.FileLayout, got.DirLayout)
			}
		})
	}
}

func TestCollectAddInputsFollowsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "target-link.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	inputs, err := collectAddInputs([]string{link})
	if err != nil {
		t.Fatalf("collect symlink input: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if inputs[0].BaseName != filepath.Base(link) {
		t.Fatalf("base name = %q, want symlink basename %q", inputs[0].BaseName, filepath.Base(link))
	}
	if !inputs[0].Info.Mode().IsRegular() {
		t.Fatalf("symlink target mode = %v, want regular file", inputs[0].Info.Mode())
	}
}

func TestAddInputsFlatUnixFSUsesMapBoundaryForSymlinkDirectory(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	rootDir := t.TempDir()
	inputRoot := filepath.Join(rootDir, "repo")
	targetDir := filepath.Join(rootDir, "target")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir input root: %v", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "note.txt"), []byte("via symlink"), 0o644); err != nil {
		t.Fatalf("write symlink target file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "plain.txt"), []byte("plain"), 0o644); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	link := filepath.Join(inputRoot, "linked")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{inputRoot}, root, addBuildOptions{
		Target: addTargetMALT,
		Model:  addModelUnixFS,
		Layout: addLayoutFlat,
	})
	if err != nil {
		t.Fatalf("add flat unixfs with symlink dir: %v", err)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want 2", result.Files)
	}

	base := filepath.Base(inputRoot)
	linkStat, err := daemon.Stat(ctx, result.NewRoot, base+"/linked")
	if err != nil {
		t.Fatalf("stat symlink dir: %v", err)
	}
	if linkStat.Kind != "dir" || linkStat.StorageKind != "map" {
		t.Fatalf("unexpected symlink dir stat: %+v", linkStat)
	}
	body, _, _, err := daemon.GetContent(ctx, result.NewRoot, base+"/linked/note.txt", "")
	if err != nil {
		t.Fatalf("read symlink target content: %v", err)
	}
	if string(body) != "via symlink" {
		t.Fatalf("symlink target body = %q", string(body))
	}
}

func TestAddInputsHierarchicalUnixFSUsesMapBoundaryForTopLevelSymlinkDirectory(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	rootDir := t.TempDir()
	targetDir := filepath.Join(rootDir, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "note.txt"), []byte("via symlink"), 0o644); err != nil {
		t.Fatalf("write symlink target file: %v", err)
	}
	link := filepath.Join(rootDir, "linked")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{link}, root, addBuildOptions{
		Target: addTargetMALT,
		Model:  addModelUnixFS,
		Layout: addLayoutHierarchical,
	})
	if err != nil {
		t.Fatalf("add hierarchical unixfs with symlink dir: %v", err)
	}
	if result.Files != 1 {
		t.Fatalf("files = %d, want 1", result.Files)
	}

	linkStat, err := daemon.Stat(ctx, result.NewRoot, "linked")
	if err != nil {
		t.Fatalf("stat symlink dir: %v", err)
	}
	if linkStat.Kind != "dir" || linkStat.StorageKind != "map" {
		t.Fatalf("unexpected symlink dir stat: %+v", linkStat)
	}

	body, _, _, err := daemon.GetContent(ctx, result.NewRoot, "linked/note.txt", "")
	if err != nil {
		t.Fatalf("read symlink target content: %v", err)
	}
	if string(body) != "via symlink" {
		t.Fatalf("symlink target body = %q", string(body))
	}
}

func TestAddInputsMALTSymlinkFileUsesRegularFilePayload(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	rootDir := t.TempDir()
	inputRoot := filepath.Join(rootDir, "repo")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir input root: %v", err)
	}
	target := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(target, []byte("via symlink file"), 0o644); err != nil {
		t.Fatalf("write symlink target file: %v", err)
	}
	link := filepath.Join(inputRoot, "linked.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{inputRoot}, root, addBuildOptions{
		Target: addTargetMALT,
		Model:  addModelUnixFS,
		Layout: addLayoutFlat,
	})
	if err != nil {
		t.Fatalf("add unixfs with symlink file: %v", err)
	}
	if result.SymlinkRoots != 0 {
		t.Fatalf("symlink roots = %d, want 0", result.SymlinkRoots)
	}

	base := filepath.Base(inputRoot)
	stat, err := daemon.Stat(ctx, result.NewRoot, base+"/linked.txt")
	if err != nil {
		t.Fatalf("stat symlink file: %v", err)
	}
	if stat.Kind != "file" || stat.StorageKind != "raw" {
		t.Fatalf("unexpected symlink file stat: %+v", stat)
	}

	resolved, err := daemon.ResolveRoot(ctx, result.NewRoot, base+"/linked.txt")
	if err != nil {
		t.Fatalf("resolve symlink file: %v", err)
	}
	var parentTarget cid.Cid
	if resolved.ProofList == nil {
		t.Fatal("resolve response did not include ProofList")
	}
	for _, step := range resolved.ProofList.Steps {
		if step.Path == base+"/linked.txt" || step.Path == "linked.txt" {
			parentTarget = step.Target
		}
	}
	if !parentTarget.Defined() {
		t.Fatalf("resolve ProofList did not expose symlink file target: %+v", resolved.ProofList)
	}
	if parentTarget.String() != stat.Key {
		t.Fatalf("symlink file parent target = %s, want stat key %s", parentTarget, stat.Key)
	}
	if codec.SemanticKindOf(parentTarget) == codec.SemanticKindMap {
		t.Fatalf("symlink file parent target should be a regular file payload, got map %s", parentTarget)
	}
	body, _, _, err := daemon.GetContent(ctx, result.NewRoot, base+"/linked.txt", "")
	if err != nil {
		t.Fatalf("read symlink file content: %v", err)
	}
	if string(body) != "via symlink file" {
		t.Fatalf("symlink file body = %q", string(body))
	}
}

func TestFormatAddSummaryUsesHumanReadableObjectCounts(t *testing.T) {
	out := formatAddSummary(addSummary{
		Target:           addTargetMALT,
		Model:            addModelUnixFS,
		Layout:           addLayoutFlat,
		NewRoot:          fakeAddCID("summary-root").String(),
		ImmutableObjects: 3,
		MALTObjects:      2,
		MALTMaps:         2,
		MALTLists:        0,
		SymlinkRoots:     1,
		Files:            1,
		Bytes:            12,
	})

	for _, want := range []string{
		"Uploaded 3 immutable objects",
		"Wrote 2 MALT objects: 2 maps, 0 lists",
		"Materialized 1 symlink root",
		"Result root:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestAddInputsWithUnixFSMerkleDAGTarget(t *testing.T) {
	ctx := context.Background()
	casClient := casmock.NewCAS(casmock.WithoutLatency())
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello merkle target"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, nil, casClient, []string{file}, "", addBuildOptions{
		Target:     addTargetMerkleDAG,
		Model:      addModelUnixFS,
		FileLayout: addFileLayoutBalanced,
		DirLayout:  addDirLayoutBasic,
	})
	if err != nil {
		t.Fatalf("add merkle-dag target: %v", err)
	}
	if result.NewRoot == "" {
		t.Fatal("new root should not be empty")
	}
	if result.Files != 1 {
		t.Fatalf("files = %d, want 1", result.Files)
	}
	root, err := cid.Decode(result.NewRoot)
	if err != nil {
		t.Fatalf("decode root: %v", err)
	}
	if _, err := casClient.Get(ctx, root); err != nil {
		t.Fatalf("root block should be present in CAS: %v", err)
	}
}

func TestMergeAddNodesConflictPolicy(t *testing.T) {
	t.Run("file-file replace", func(t *testing.T) {
		existing := newDirNode()
		staged := newDirNode()
		setFileNode(existing, "docs/readme.md", fakeAddCID("v1"))
		setFileNode(staged, "docs/readme.md", fakeAddCID("v2"))

		merged := mergeAddNodes(existing, staged)
		got := mustAddNodeAtPath(t, merged, "docs/readme.md")
		if got.Kind != "file" || !got.Key.Equals(fakeAddCID("v2")) {
			t.Fatalf("merged node = %+v", got)
		}
	})

	t.Run("dir-dir merge", func(t *testing.T) {
		existing := newDirNode()
		staged := newDirNode()
		setFileNode(existing, "docs/guide.txt", fakeAddCID("guide"))
		setFileNode(staged, "docs/new.txt", fakeAddCID("new"))

		merged := mergeAddNodes(existing, staged)
		if mustAddNodeAtPath(t, merged, "docs/guide.txt").Kind != "file" {
			t.Fatal("expected existing child to remain after dir merge")
		}
		if mustAddNodeAtPath(t, merged, "docs/new.txt").Kind != "file" {
			t.Fatal("expected staged child to appear after dir merge")
		}
	})

	t.Run("file-dir replace subtree", func(t *testing.T) {
		existing := newDirNode()
		staged := newDirNode()
		setFileNode(existing, "docs", fakeAddCID("leaf"))
		ensureDirNode(staged, "docs/subdir")
		setFileNode(staged, "docs/subdir/readme.md", fakeAddCID("nested"))

		merged := mergeAddNodes(existing, staged)
		docs := mustAddNodeAtPath(t, merged, "docs")
		if docs.Kind != "dir" {
			t.Fatalf("docs.Kind = %q, want dir", docs.Kind)
		}
		if mustAddNodeAtPath(t, merged, "docs/subdir/readme.md").Kind != "file" {
			t.Fatal("expected subtree replacement to keep nested staged file")
		}
	})

	t.Run("dir-file replace subtree", func(t *testing.T) {
		existing := newDirNode()
		staged := newDirNode()
		setFileNode(existing, "docs/guide.txt", fakeAddCID("guide"))
		setFileNode(staged, "docs", fakeAddCID("flat"))

		merged := mergeAddNodes(existing, staged)
		docs := mustAddNodeAtPath(t, merged, "docs")
		if docs.Kind != "file" {
			t.Fatalf("docs.Kind = %q, want file", docs.Kind)
		}
		if _, ok := docs.Children["guide.txt"]; ok {
			t.Fatal("directory subtree should be replaced by file node")
		}
	})
}

func TestAddWorkflowMaterializesSmallLargeAndEmptyDir(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(inputRoot, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(inputRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "nested", "small.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write small: %v", err)
	}
	large := make([]byte, addFixedChunkSize+17)
	for i := range large {
		large[i] = byte('a' + (i % 7))
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "nested", "large.bin"), large, 0o644); err != nil {
		t.Fatalf("write large: %v", err)
	}

	staged, err := buildAddStagingTree(ctx, casClient, daemon, []string{inputRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build staging: %v", err)
	}
	if staged.Files != 2 {
		t.Fatalf("staged files = %d, want 2", staged.Files)
	}

	merged := mergeAddNodes(newDirNode(), staged.Root)
	mat, err := materializeDirectory(ctx, daemon, casClient, merged)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	base := filepath.Base(inputRoot)
	emptyStat, err := daemon.Stat(ctx, mat.Key.String(), base+"/empty")
	if err != nil {
		t.Fatalf("stat empty dir: %v", err)
	}
	if emptyStat.Kind != "dir" {
		t.Fatalf("empty kind = %q, want dir", emptyStat.Kind)
	}
	if emptyStat.Payload == "" {
		t.Fatal("empty dir payload should not be empty")
	}

	smallStat, err := daemon.Stat(ctx, mat.Key.String(), base+"/nested/small.txt")
	if err != nil {
		t.Fatalf("stat small: %v", err)
	}
	if smallStat.StorageKind != "raw" {
		t.Fatalf("small storage kind = %q, want raw", smallStat.StorageKind)
	}

	largeStat, err := daemon.Stat(ctx, mat.Key.String(), base+"/nested/large.bin")
	if err != nil {
		t.Fatalf("stat large: %v", err)
	}
	if largeStat.StorageKind != "list" {
		t.Fatalf("large storage kind = %q, want list", largeStat.StorageKind)
	}
}

func TestAddCASBatcherDeduplicatesBlocks(t *testing.T) {
	ctx := context.Background()
	recorder := newRecordingAddCAS(casmock.NewCAS(casmock.WithoutLatency()))
	batcher := newAddCASBatcher(recorder)

	first, err := batcher.Put(ctx, []byte("same"))
	if err != nil {
		t.Fatalf("Put first: %v", err)
	}
	second, err := batcher.Put(ctx, []byte("same"))
	if err != nil {
		t.Fatalf("Put duplicate: %v", err)
	}
	if !first.Equals(second) {
		t.Fatalf("duplicate CID = %s, want %s", second, first)
	}
	typed, err := batcher.PutWithCodec(ctx, []byte(`{"entries":["a.txt"]}`), codec.CodecMaltManifest)
	if err != nil {
		t.Fatalf("PutWithCodec: %v", err)
	}
	if typed.Prefix().Codec != codec.CodecMaltManifest {
		t.Fatalf("typed codec = %x, want %x", typed.Prefix().Codec, codec.CodecMaltManifest)
	}
	if err := batcher.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(recorder.batches) != 1 {
		t.Fatalf("batch calls = %d, want 1", len(recorder.batches))
	}
	if len(recorder.batches[0]) != 2 {
		t.Fatalf("batched blocks = %d, want 2", len(recorder.batches[0]))
	}
}

func TestBuildAddStagingTreeFlushesCASBatchBeforeRootMaterialization(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	recorder := newRecordingAddCAS(casClient)

	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "a.txt"), []byte("duplicate"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "b.txt"), []byte("duplicate"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	large := make([]byte, addFixedChunkSize+11)
	for i := range large {
		large[i] = byte('a' + (i % 17))
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "large.bin"), large, 0o644); err != nil {
		t.Fatalf("write large: %v", err)
	}

	staged, err := buildAddStagingTree(ctx, recorder, daemon, []string{inputRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build staging: %v", err)
	}
	if staged.Files != 3 {
		t.Fatalf("files = %d, want 3", staged.Files)
	}
	if len(recorder.batches) == 0 {
		t.Fatal("expected staged CAS writes to flush through PutBatch")
	}
	payloadCopies := 0
	for _, batch := range recorder.batches {
		for _, block := range batch {
			if string(block.Data) == "duplicate" {
				payloadCopies++
			}
		}
	}
	if payloadCopies != 1 {
		t.Fatalf("deduplicated small-file payload copies = %d, want 1", payloadCopies)
	}

	mat, err := materializeDirectory(ctx, daemon, recorder, staged.Root)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	base := filepath.Base(inputRoot)
	for _, p := range []string{base + "/a.txt", base + "/b.txt", base + "/large.bin"} {
		if _, err := daemon.Stat(ctx, mat.Key.String(), p); err != nil {
			t.Fatalf("stat %q: %v", p, err)
		}
	}
	body, _, _, err := daemon.GetContent(ctx, mat.Key.String(), base+"/a.txt", "")
	if err != nil {
		t.Fatalf("get a.txt: %v", err)
	}
	if string(body) != "duplicate" {
		t.Fatalf("a.txt body = %q, want duplicate", body)
	}
	largeBody, _, _, err := daemon.GetContent(ctx, mat.Key.String(), base+"/large.bin", "")
	if err != nil {
		t.Fatalf("get large.bin: %v", err)
	}
	if len(largeBody) != len(large) || string(largeBody[:64]) != string(large[:64]) {
		t.Fatal("large body mismatch")
	}
}

func TestAddInputsWithUnixFSUsesCASPutBatch(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	recorder := newRecordingAddCAS(casClient)

	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "a.txt"), []byte("hello batch"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, recorder, []string{inputRoot}, "", addBuildOptions{})
	if err != nil {
		t.Fatalf("add with unixfs: %v", err)
	}
	if result.NewRoot == "" {
		t.Fatal("new root should not be empty")
	}
	if len(recorder.batches) == 0 {
		t.Fatal("expected normal MALT add path to call PutBatch")
	}
	body, _, _, err := daemon.GetContent(ctx, result.NewRoot, filepath.Base(inputRoot)+"/a.txt", "")
	if err != nil {
		t.Fatalf("get a.txt: %v", err)
	}
	if string(body) != "hello batch" {
		t.Fatalf("body = %q, want hello batch", body)
	}
}

func TestAddInputsWithUnixFSDeduplicatesDuplicateSmallPayloads(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	recorder := newRecordingAddCAS(casClient)

	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	payload := []byte("same payload")
	if err := os.WriteFile(filepath.Join(inputRoot, "a.txt"), payload, 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "b.txt"), payload, 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, recorder, []string{inputRoot}, "", addBuildOptions{})
	if err != nil {
		t.Fatalf("add with unixfs: %v", err)
	}
	payloadCopies := 0
	for _, batch := range recorder.batches {
		for _, block := range batch {
			if string(block.Data) == string(payload) {
				payloadCopies++
			}
		}
	}
	if payloadCopies != 1 {
		t.Fatalf("duplicate payload copies uploaded = %d, want 1", payloadCopies)
	}

	base := filepath.Base(inputRoot)
	for _, name := range []string{"a.txt", "b.txt"} {
		body, _, _, err := daemon.GetContent(ctx, result.NewRoot, base+"/"+name, "")
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if string(body) != string(payload) {
			t.Fatalf("%s body = %q, want %q", name, body, payload)
		}
	}
}

func TestAddInputsWithUnixFSLargeFileThroughStaging(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	recorder := newRecordingAddCAS(casClient)

	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	large := make([]byte, addFixedChunkSize+31)
	for i := range large {
		large[i] = byte('a' + (i % 19))
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "large.bin"), large, 0o644); err != nil {
		t.Fatalf("write large: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, recorder, []string{inputRoot}, "", addBuildOptions{})
	if err != nil {
		t.Fatalf("add with unixfs: %v", err)
	}
	base := filepath.Base(inputRoot)
	stat, err := daemon.Stat(ctx, result.NewRoot, base+"/large.bin")
	if err != nil {
		t.Fatalf("stat large: %v", err)
	}
	if stat.Kind != "file" || stat.StorageKind != "list" {
		t.Fatalf("unexpected large stat: %+v", stat)
	}
	if stat.Size == nil || *stat.Size != int64(len(large)) {
		t.Fatalf("large size = %v, want %d", stat.Size, len(large))
	}
	body, _, _, err := daemon.GetContent(ctx, result.NewRoot, base+"/large.bin", "")
	if err != nil {
		t.Fatalf("get large: %v", err)
	}
	if len(body) != len(large) || string(body[:64]) != string(large[:64]) {
		t.Fatal("large body mismatch")
	}

	rangeStart := addFixedChunkSize - 2
	rangeEndExclusive := addFixedChunkSize + 2
	rangeHeader := fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEndExclusive-1)
	rangeBody, status, headers, err := daemon.GetContent(ctx, result.NewRoot, base+"/large.bin", rangeHeader)
	if err != nil {
		t.Fatalf("get large range: %v", err)
	}
	if status != http.StatusPartialContent {
		t.Fatalf("large range status = %d, want %d", status, http.StatusPartialContent)
	}
	if string(rangeBody) != string(large[rangeStart:rangeEndExclusive]) {
		t.Fatalf("large range body = %q, want %q", rangeBody, large[rangeStart:rangeEndExclusive])
	}
	pl, err := daemonclient.ProofListFromHeaders(headers)
	if err != nil {
		t.Fatalf("large range prooflist: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("large range prooflist shape: %v", err)
	}
	hasRangeStep := false
	for _, step := range pl.Steps {
		if step.Kind == prooflist.KindListRange {
			hasRangeStep = true
			break
		}
	}
	if !hasRangeStep {
		t.Fatalf("large range ProofList is missing list_range step: %+v", pl.Steps)
	}
}

func TestAddInputsWithUnixFSWorkflow(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(inputRoot, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(inputRoot, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "nested", "small.txt"), []byte("hello unixfs"), 0o644); err != nil {
		t.Fatalf("write small: %v", err)
	}
	large := make([]byte, addFixedChunkSize+23)
	for i := range large {
		large[i] = byte('a' + (i % 13))
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "nested", "large.bin"), large, 0o644); err != nil {
		t.Fatalf("write large: %v", err)
	}

	result, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{inputRoot}, root, addBuildOptions{})
	if err != nil {
		t.Fatalf("add with unixfs: %v", err)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want 2", result.Files)
	}
	if result.NewRoot == "" {
		t.Fatal("new root should not be empty")
	}

	base := filepath.Base(inputRoot)
	for _, p := range []string{base, base + "/empty", base + "/nested", base + "/nested/small.txt", base + "/nested/large.bin"} {
		if _, err := daemon.Stat(ctx, result.NewRoot, p); err != nil {
			t.Fatalf("stat %q of root %q: %v", p, result.NewRoot, err)
		}
	}
	baseStat, err := daemon.Stat(ctx, result.NewRoot, base)
	if err != nil {
		t.Fatalf("stat base: %v", err)
	}
	if baseStat.Kind != "dir" {
		t.Fatalf("base kind = %q, want dir", baseStat.Kind)
	}
	if _, err := daemon.Stat(ctx, result.NewRoot, base+"/missing.txt"); err == nil {
		t.Fatal("missing child under manifest directory should not stat successfully")
	}

	emptyStat, err := daemon.Stat(ctx, result.NewRoot, base+"/empty")
	if err != nil {
		t.Fatalf("stat empty: %v", err)
	}
	if emptyStat.Kind != "dir" || emptyStat.StorageKind != "map" {
		t.Fatalf("unexpected empty stat: %+v", emptyStat)
	}

	smallStat, err := daemon.Stat(ctx, result.NewRoot, base+"/nested/small.txt")
	if err != nil {
		t.Fatalf("stat small: %v", err)
	}
	if smallStat.Kind != "file" || smallStat.StorageKind != "raw" {
		t.Fatalf("unexpected small stat: %+v", smallStat)
	}
	body, _, _, err := daemon.GetContent(ctx, result.NewRoot, base+"/nested/small.txt", "")
	if err != nil {
		t.Fatalf("get small content: %v", err)
	}
	if string(body) != "hello unixfs" {
		t.Fatalf("small body = %q", string(body))
	}

	largeStat, err := daemon.Stat(ctx, result.NewRoot, base+"/nested/large.bin")
	if err != nil {
		t.Fatalf("stat large: %v", err)
	}
	if largeStat.Kind != "file" || largeStat.StorageKind != "list" {
		t.Fatalf("unexpected large stat: %+v", largeStat)
	}
	largeBody, _, _, err := daemon.GetContent(ctx, result.NewRoot, base+"/nested/large.bin", "")
	if err != nil {
		t.Fatalf("get large content: %v", err)
	}
	if len(largeBody) != len(large) || string(largeBody[:64]) != string(large[:64]) {
		t.Fatal("large body mismatch")
	}

}

func TestAddResolveVerifyDaemonClientFlow(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	root := newTestRoot(ctx, t, daemon, casClient)
	inputRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(inputRoot, 0o755); err != nil {
		t.Fatalf("mkdir input root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputRoot, "note.txt"), []byte("daemon-client proof"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	added, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{inputRoot}, root, addBuildOptions{})
	if err != nil {
		t.Fatalf("add with unixfs: %v", err)
	}
	if added.NewRoot == "" {
		t.Fatal("add result new root is empty")
	}

	proofJSON := captureStdout(t, func() {
		if err := runResolve(testCommandWithContext(ctx), []string{added.NewRoot, filepath.Base(inputRoot) + "/note.txt"}); err != nil {
			t.Fatalf("run resolve: %v", err)
		}
	})
	if !strings.Contains(proofJSON, `"prooflist"`) {
		t.Fatalf("resolve output missing prooflist:\n%s", proofJSON)
	}

	proofPath := filepath.Join(t.TempDir(), "resolve-proof.json")
	if err := os.WriteFile(proofPath, []byte(proofJSON), 0o644); err != nil {
		t.Fatalf("write resolve proof: %v", err)
	}

	cmd := testCommandWithContext(ctx)
	cmd.Flags().String("prooflist", proofPath, "")
	verifyOut := captureStdout(t, func() {
		if err := runVerify(cmd, nil); err != nil {
			t.Fatalf("run verify: %v", err)
		}
	})
	if !strings.Contains(verifyOut, "valid: true") {
		t.Fatalf("verify output = %q, want valid true", verifyOut)
	}
}

func TestAddInputsWithUnixFSAddsIncrementally(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	legacyRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(legacyRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir legacy docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "docs", "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	result, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{legacyRoot}, root, addBuildOptions{})
	if err != nil {
		t.Fatalf("add initial files: %v", err)
	}

	nextRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(nextRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir next docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nextRoot, "docs", "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write next file: %v", err)
	}
	result2, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{nextRoot}, result.NewRoot, addBuildOptions{})
	if err != nil {
		t.Fatalf("add incremental files: %v", err)
	}

	base := filepath.Base(legacyRoot)
	oldBody, _, _, err := daemon.GetContent(ctx, result2.NewRoot, base+"/docs/old.txt", "")
	if err != nil {
		t.Fatalf("read migrated old file: %v", err)
	}
	if string(oldBody) != "old" {
		t.Fatalf("old body = %q", string(oldBody))
	}
	newBody, _, _, err := daemon.GetContent(ctx, result2.NewRoot, base+"/docs/new.txt", "")
	if err != nil {
		t.Fatalf("read new file: %v", err)
	}
	if string(newBody) != "new" {
		t.Fatalf("new body = %q", string(newBody))
	}
}

func TestAddWorkflowMergesExistingTree(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	firstRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(firstRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, "README.md"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write readme v1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, "docs", "guide.txt"), []byte("guide"), 0o644); err != nil {
		t.Fatalf("write guide: %v", err)
	}

	firstStaged, err := buildAddStagingTree(ctx, casClient, daemon, []string{firstRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build first staging: %v", err)
	}
	firstMerged := mergeAddNodes(newDirNode(), firstStaged.Root)
	firstMat, err := materializeDirectory(ctx, daemon, casClient, firstMerged)
	if err != nil {
		t.Fatalf("materialize first: %v", err)
	}

	existing, err := loadExistingCurrentTree(ctx, daemon, casClient, firstMat.Key.String())
	if err != nil {
		t.Fatalf("load existing tree: %v", err)
	}

	secondRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(secondRoot, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs second: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "README.md"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write readme v2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secondRoot, "docs", "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write new: %v", err)
	}
	secondStaged, err := buildAddStagingTree(ctx, casClient, daemon, []string{secondRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build second staging: %v", err)
	}

	secondMerged := mergeAddNodes(existing, secondStaged.Root)
	secondMat, err := materializeDirectory(ctx, daemon, casClient, secondMerged)
	if err != nil {
		t.Fatalf("materialize second: %v", err)
	}
	mergedRoot := secondMat.Key.String()

	base := filepath.Base(secondRoot)
	readmeStat, err := daemon.Stat(ctx, mergedRoot, base+"/README.md")
	if err != nil {
		t.Fatalf("stat readme: %v", err)
	}
	body, _, _, err := daemon.GetContent(ctx, mergedRoot, base+"/README.md", "")
	if err != nil {
		t.Fatalf("get readme content: %v", err)
	}
	if string(body) != "v2" {
		t.Fatalf("readme content = %q, want %q", string(body), "v2")
	}
	if readmeStat.Kind != "file" {
		t.Fatalf("readme kind = %q, want file", readmeStat.Kind)
	}

	if _, err := daemon.Stat(ctx, mergedRoot, base+"/docs/guide.txt"); err != nil {
		t.Fatalf("guide should remain after merge: %v", err)
	}
	if _, err := daemon.Stat(ctx, mergedRoot, base+"/docs/new.txt"); err != nil {
		t.Fatalf("new file should exist after merge: %v", err)
	}
}

func TestAddInputsWithUnixFSRootReplacesExistingDirWithSymlinkDirBoundary(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	firstRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(firstRoot, "linked"), 0o755); err != nil {
		t.Fatalf("mkdir linked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(firstRoot, "linked", "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	first, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{firstRoot}, root, addBuildOptions{})
	if err != nil {
		t.Fatalf("add initial directory: %v", err)
	}

	rootDir := t.TempDir()
	secondRoot := filepath.Join(rootDir, "repo")
	targetDir := filepath.Join(rootDir, "target")
	if err := os.MkdirAll(secondRoot, 0o755); err != nil {
		t.Fatalf("mkdir second root: %v", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir symlink target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(secondRoot, "linked")); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	second, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{secondRoot}, first.NewRoot, addBuildOptions{})
	if err != nil {
		t.Fatalf("add symlink directory over existing directory: %v", err)
	}

	base := filepath.Base(secondRoot)
	linkStat, err := daemon.Stat(ctx, second.NewRoot, base+"/linked")
	if err != nil {
		t.Fatalf("stat symlink dir boundary: %v", err)
	}
	if linkStat.Kind != "dir" || linkStat.StorageKind != "map" {
		t.Fatalf("unexpected symlink dir stat: %+v", linkStat)
	}
	body, _, _, err := daemon.GetContent(ctx, second.NewRoot, base+"/linked/new.txt", "")
	if err != nil {
		t.Fatalf("read symlink directory replacement content: %v", err)
	}
	if string(body) != "new" {
		t.Fatalf("new body = %q", string(body))
	}
	if _, err := daemon.Stat(ctx, second.NewRoot, base+"/linked/old.txt"); err == nil {
		t.Fatal("old directory child should be replaced by symlink directory boundary")
	}
}

func newAddTestClients(t *testing.T) (*daemonclient.Client, *ipfs.Client) {
	t.Helper()

	mockCAS := casmock.NewCAS(casmock.WithoutLatency())
	mockHTTP := casmock.NewHTTPServer("127.0.0.1:0", mockCAS)
	casTS := httptest.NewServer(mockHTTP.Handler())
	t.Cleanup(casTS.Close)

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = casTS.URL

	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})

	ts := httptest.NewServer(server.New(node, "127.0.0.1:0").Handler())
	t.Cleanup(ts.Close)
	return daemonclient.NewWithBaseURL(ts.URL), ipfs.NewClient(casTS.URL)
}

type recordingAddCAS struct {
	inner   addCASClient
	batches [][]cas.Block
}

func newRecordingAddCAS(inner addCASClient) *recordingAddCAS {
	return &recordingAddCAS{inner: inner}
}

func (r *recordingAddCAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return r.inner.Put(ctx, data)
}

func (r *recordingAddCAS) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	return r.inner.PutWithCodec(ctx, data, codec)
}

func (r *recordingAddCAS) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	return r.inner.Get(ctx, c)
}

func (r *recordingAddCAS) PutBatch(ctx context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	cloned := make([]cas.Block, len(blocks))
	for i, block := range blocks {
		cloned[i] = cas.Block{Data: append([]byte(nil), block.Data...), Codec: block.Codec}
	}
	r.batches = append(r.batches, cloned)
	if batch, ok := r.inner.(cas.BatchWriter); ok {
		return batch.PutBatch(ctx, blocks)
	}
	return cas.PutBlocks(ctx, r.inner, blocks)
}

func mustAddNodeAtPath(t *testing.T, root *addNode, p string) *addNode {
	t.Helper()
	cur := root
	for _, part := range splitAddPath(p) {
		if cur == nil || cur.Children == nil {
			t.Fatalf("missing node at %q", p)
		}
		next, ok := cur.Children[part]
		if !ok {
			t.Fatalf("missing path segment %q in %q", part, p)
		}
		cur = next
	}
	if cur == nil {
		t.Fatalf("nil node at %q", p)
	}
	return cur
}

func fakeAddCID(seed string) cid.Cid {
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

// newTestRoot creates a root structure with a valid @payload in the CAS.
// The returned root can be used for UnixFS operations since the migration path
// can read the @payload directory manifest from the mock CAS.
func newTestRoot(ctx context.Context, t *testing.T, daemon *daemonclient.Client, casClient *ipfs.Client) string {
	t.Helper()

	manifestData := []byte(`{"entries":["dummy"]}`)
	manifestCID, err := casClient.Put(ctx, manifestData)
	if err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	dummyData := []byte("dummy")
	dummyCID, err := casClient.Put(ctx, dummyData)
	if err != nil {
		t.Fatalf("put dummy: %v", err)
	}

	resp, err := daemon.CreateRootStructure(ctx, map[string]string{
		"@payload": manifestCID.String(),
		"dummy":    dummyCID.String(),
	})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}
	return resp.Root
}
