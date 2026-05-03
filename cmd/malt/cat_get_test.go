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

	root := newTestRoot(ctx, t, daemon, casClient)

	// Chain UnixFS operations: each returns a new root.
	resp1, err := daemon.AddUnixFSDirectory(ctx, root, "dir/empty")
	if err != nil {
		t.Fatalf("create empty dir: %v", err)
	}
	resp2, err := daemon.AddUnixFSFile(ctx, resp1.NewRoot, "root.txt", []byte("root"))
	if err != nil {
		t.Fatalf("write root file: %v", err)
	}
	large := make([]byte, addFixedChunkSize+33)
	for i := range large {
		large[i] = byte('k' + (i % 11))
	}
	resp3, err := daemon.AddUnixFSFile(ctx, resp2.NewRoot, "dir/large.bin", large)
	if err != nil {
		t.Fatalf("write large file: %v", err)
	}
	finalRoot := resp3.NewRoot

	rootStat, err := daemon.Stat(ctx, finalRoot, "")
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}

	outDir := filepath.Join(t.TempDir(), "out")
	if err := exportDirectory(ctx, daemon, casClient, finalRoot, "", outDir, rootStat); err != nil {
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

	root := newTestRoot(ctx, t, daemon, casClient)

	resp1, err := daemon.AddUnixFSFile(ctx, root, "small.txt", []byte("hello-cat"))
	if err != nil {
		t.Fatalf("write small file: %v", err)
	}
	large := make([]byte, addFixedChunkSize+5)
	for i := range large {
		large[i] = byte('a' + (i % 5))
	}
	resp2, err := daemon.AddUnixFSFile(ctx, resp1.NewRoot, "large.bin", large)
	if err != nil {
		t.Fatalf("write large file: %v", err)
	}
	finalRoot := resp2.NewRoot

	outSmall := filepath.Join(t.TempDir(), "small.out")
	if err := writeContentFile(ctx, daemon, finalRoot, "small.txt", outSmall); err != nil {
		t.Fatalf("write small file: %v", err)
	}
	gotSmall, err := os.ReadFile(outSmall)
	if err != nil {
		t.Fatalf("read small output: %v", err)
	}
	if string(gotSmall) != "hello-cat" {
		t.Fatalf("small output = %q, want %q", string(gotSmall), "hello-cat")
	}

	outLarge := filepath.Join(t.TempDir(), "large.out")
	if err := writeContentFile(ctx, daemon, finalRoot, "large.bin", outLarge); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	gotLarge, err := os.ReadFile(outLarge)
	if err != nil {
		t.Fatalf("read large output: %v", err)
	}
	if len(gotLarge) != len(large) {
		t.Fatalf("large output len = %d, want %d", len(gotLarge), len(large))
	}
}
