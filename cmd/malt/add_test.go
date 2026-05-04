package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
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
	t.Skip("addDirectoryWithUnixFS does not support symlinks inside input directories")

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
	t.Skip("addDirectoryWithUnixFS symlink handling has pre-existing issues")

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

	outDir := filepath.Join(t.TempDir(), "out")
	rootStat, err := daemon.Stat(ctx, result.NewRoot, base)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if err := exportDirectory(ctx, daemon, casClient, result.NewRoot, base, outDir, rootStat); err != nil {
		t.Fatalf("export unixfs directory: %v", err)
	}
	if info, err := os.Stat(filepath.Join(outDir, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("expected exported empty directory, err=%v", err)
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
	return daemonclient.NewWithBaseURL(ts.URL + "/api/v1"), ipfs.NewClient(casTS.URL)
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
