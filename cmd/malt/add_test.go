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

func TestCollectAddInputsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "target-link.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	if _, err := collectAddInputs([]string{link}); err == nil {
		t.Fatal("expected symlink input to be rejected")
	}
}

func TestStageDirectoryInputRejectsNestedSymlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(src, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported in test environment: %v", err)
	}

	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat src: %v", err)
	}
	daemon, casClient := newAddTestClients(t)
	if _, err := daemon.CreateBucket(context.Background(), "demo", ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	_, _, err = stageDirectoryInput(context.Background(), newDirNode(), casClient, daemon, "demo", addMountedInput{
		Input: addInput{
			Original: src,
			AbsPath:  src,
			BaseName: filepath.Base(src),
			Info:     info,
		},
		MountBase: "src",
	})
	if err == nil {
		t.Fatal("expected nested symlink to be rejected")
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
	bucketID := "demo"

	if _, err := daemon.CreateBucket(ctx, bucketID, ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

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

	staged, err := buildAddStagingTree(ctx, casClient, daemon, bucketID, []string{inputRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build staging: %v", err)
	}
	if staged.Files != 2 {
		t.Fatalf("staged files = %d, want 2", staged.Files)
	}

	merged := mergeAddNodes(newDirNode(), staged.Root)
	mat, err := materializeDirectory(ctx, daemon, casClient, bucketID, merged)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := daemon.SetBucketHead(ctx, bucketID, mat.Key.String(), mat.ArcCount, ""); err != nil {
		t.Fatalf("set head: %v", err)
	}

	base := filepath.Base(inputRoot)
	emptyStat, err := daemon.StatBucketPath(ctx, bucketID, base+"/empty")
	if err != nil {
		t.Fatalf("stat empty dir: %v", err)
	}
	if emptyStat.Kind != "dir" {
		t.Fatalf("empty kind = %q, want dir", emptyStat.Kind)
	}
	if emptyStat.Payload == "" {
		t.Fatal("empty dir payload should not be empty")
	}

	smallStat, err := daemon.StatBucketPath(ctx, bucketID, base+"/nested/small.txt")
	if err != nil {
		t.Fatalf("stat small: %v", err)
	}
	if smallStat.StorageKind != "raw" {
		t.Fatalf("small storage kind = %q, want raw", smallStat.StorageKind)
	}

	largeStat, err := daemon.StatBucketPath(ctx, bucketID, base+"/nested/large.bin")
	if err != nil {
		t.Fatalf("stat large: %v", err)
	}
	if largeStat.StorageKind != "list" {
		t.Fatalf("large storage kind = %q, want list", largeStat.StorageKind)
	}
}

func TestAddWorkflowMergesExistingTree(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	bucketID := "merge-demo"
	if _, err := daemon.CreateBucket(ctx, bucketID, ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

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

	firstStaged, err := buildAddStagingTree(ctx, casClient, daemon, bucketID, []string{firstRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build first staging: %v", err)
	}
	firstMerged := mergeAddNodes(newDirNode(), firstStaged.Root)
	firstMat, err := materializeDirectory(ctx, daemon, casClient, bucketID, firstMerged)
	if err != nil {
		t.Fatalf("materialize first: %v", err)
	}
	if err := daemon.SetBucketHead(ctx, bucketID, firstMat.Key.String(), firstMat.ArcCount, ""); err != nil {
		t.Fatalf("set first head: %v", err)
	}

	meta, err := daemon.GetBucket(ctx, bucketID)
	if err != nil {
		t.Fatalf("get bucket meta: %v", err)
	}
	existing, err := loadExistingBucketTree(ctx, daemon, casClient, bucketID, meta.Root)
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
	secondStaged, err := buildAddStagingTree(ctx, casClient, daemon, bucketID, []string{secondRoot}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build second staging: %v", err)
	}

	secondMerged := mergeAddNodes(existing, secondStaged.Root)
	secondMat, err := materializeDirectory(ctx, daemon, casClient, bucketID, secondMerged)
	if err != nil {
		t.Fatalf("materialize second: %v", err)
	}
	if err := daemon.SetBucketHead(ctx, bucketID, secondMat.Key.String(), secondMat.ArcCount, meta.Root); err != nil {
		t.Fatalf("set second head: %v", err)
	}

	base := filepath.Base(secondRoot)
	readmeStat, err := daemon.StatBucketPath(ctx, bucketID, base+"/README.md")
	if err != nil {
		t.Fatalf("stat readme: %v", err)
	}
	body, status, _, err := daemon.GetBucketContent(ctx, bucketID, base+"/README.md", "")
	if err != nil {
		t.Fatalf("get readme content: status=%d err=%v", status, err)
	}
	if string(body) != "v2" {
		t.Fatalf("readme content = %q, want %q", string(body), "v2")
	}
	if readmeStat.Kind != "file" {
		t.Fatalf("readme kind = %q, want file", readmeStat.Kind)
	}

	if _, err := daemon.StatBucketPath(ctx, bucketID, base+"/docs/guide.txt"); err != nil {
		t.Fatalf("guide should remain after merge: %v", err)
	}
	if _, err := daemon.StatBucketPath(ctx, bucketID, base+"/docs/new.txt"); err != nil {
		t.Fatalf("new file should exist after merge: %v", err)
	}
}

func newAddTestClients(t *testing.T) (*daemonclient.Client, *ipfs.Client) {
	t.Helper()

	mockCAS := casmock.NewCAS()
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
