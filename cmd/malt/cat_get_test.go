package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveGetOutputPathRules(t *testing.T) {
	if _, err := resolveGetOutputPath("", "dir", ""); err == nil {
		t.Fatal("expected root path without explicit output to fail")
	}
	got, err := resolveGetOutputPath("docs/readme.md", "file", "")
	if err != nil {
		t.Fatalf("resolve output path: %v", err)
	}
	if got != filepath.Join(".", "readme.md") {
		t.Fatalf("output = %q, want %q", got, filepath.Join(".", "readme.md"))
	}
	got, err = resolveGetOutputPath("assets", "dir", "")
	if err != nil {
		t.Fatalf("resolve dir output path: %v", err)
	}
	if got != filepath.Join(".", "assets") {
		t.Fatalf("output = %q, want %q", got, filepath.Join(".", "assets"))
	}
	got, err = resolveGetOutputPath("", "dir", "out")
	if err != nil {
		t.Fatalf("explicit output should pass: %v", err)
	}
	if got != "out" {
		t.Fatalf("explicit output = %q, want %q", got, "out")
	}
}

func TestGetExportDirectoryMixedTree(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	bucketID := "get-export"
	if _, err := daemon.CreateBucket(ctx, bucketID, ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	source := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(source, "dir", "empty"), 0o755); err != nil {
		t.Fatalf("mkdir source dirs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "root.txt"), []byte("root"), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	large := make([]byte, addFixedChunkSize+33)
	for i := range large {
		large[i] = byte('k' + (i % 11))
	}
	if err := os.WriteFile(filepath.Join(source, "dir", "large.bin"), large, 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	staged, err := buildAddStagingTree(ctx, casClient, daemon, bucketID, []string{source}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build staging: %v", err)
	}
	merged := mergeAddNodes(newDirNode(), staged.Root)
	mat, err := materializeDirectory(ctx, daemon, casClient, bucketID, merged)
	if err != nil {
		t.Fatalf("materialize root: %v", err)
	}
	if err := daemon.SetBucketHead(ctx, bucketID, mat.Key.String(), mat.ArcCount, ""); err != nil {
		t.Fatalf("set bucket head: %v", err)
	}

	base := filepath.Base(source)
	rootStat, err := daemon.StatBucketPath(ctx, bucketID, base)
	if err != nil {
		t.Fatalf("stat exported root: %v", err)
	}
	outDir := filepath.Join(t.TempDir(), "out")
	if err := exportBucketDirectory(ctx, daemon, casClient, bucketID, base, outDir, rootStat); err != nil {
		t.Fatalf("export directory: %v", err)
	}

	rootBytes, err := os.ReadFile(filepath.Join(outDir, "root.txt"))
	if err != nil {
		t.Fatalf("read exported root.txt: %v", err)
	}
	if string(rootBytes) != "root" {
		t.Fatalf("root.txt = %q, want %q", string(rootBytes), "root")
	}
	largeBytes, err := os.ReadFile(filepath.Join(outDir, "dir", "large.bin"))
	if err != nil {
		t.Fatalf("read exported large.bin: %v", err)
	}
	if len(largeBytes) != len(large) {
		t.Fatalf("large.bin length = %d, want %d", len(largeBytes), len(large))
	}
	if string(largeBytes[:64]) != string(large[:64]) {
		t.Fatal("large.bin prefix mismatch")
	}
	if info, err := os.Stat(filepath.Join(outDir, "dir", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("expected exported empty directory, err=%v", err)
	}
}

func TestWriteBucketFileSmallAndLarge(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	bucketID := "cat-stream"
	if _, err := daemon.CreateBucket(ctx, bucketID, ""); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	source := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "small.txt"), []byte("hello-cat"), 0o644); err != nil {
		t.Fatalf("write small file: %v", err)
	}
	large := make([]byte, addFixedChunkSize+5)
	for i := range large {
		large[i] = byte('a' + (i % 5))
	}
	if err := os.WriteFile(filepath.Join(source, "large.bin"), large, 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	staged, err := buildAddStagingTree(ctx, casClient, daemon, bucketID, []string{source}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build staging: %v", err)
	}
	merged := mergeAddNodes(newDirNode(), staged.Root)
	mat, err := materializeDirectory(ctx, daemon, casClient, bucketID, merged)
	if err != nil {
		t.Fatalf("materialize root: %v", err)
	}
	if err := daemon.SetBucketHead(ctx, bucketID, mat.Key.String(), mat.ArcCount, ""); err != nil {
		t.Fatalf("set bucket head: %v", err)
	}

	base := filepath.Base(source)
	outSmall := filepath.Join(t.TempDir(), "small.out")
	if err := writeBucketFile(ctx, daemon, bucketID, base+"/small.txt", outSmall); err != nil {
		t.Fatalf("write small bucket file: %v", err)
	}
	gotSmall, err := os.ReadFile(outSmall)
	if err != nil {
		t.Fatalf("read small output: %v", err)
	}
	if string(gotSmall) != "hello-cat" {
		t.Fatalf("small output = %q, want %q", string(gotSmall), "hello-cat")
	}

	outLarge := filepath.Join(t.TempDir(), "large.out")
	if err := writeBucketFile(ctx, daemon, bucketID, base+"/large.bin", outLarge); err != nil {
		t.Fatalf("write large bucket file: %v", err)
	}
	gotLarge, err := os.ReadFile(outLarge)
	if err != nil {
		t.Fatalf("read large output: %v", err)
	}
	if len(gotLarge) != len(large) {
		t.Fatalf("large output len = %d, want %d", len(gotLarge), len(large))
	}
}

