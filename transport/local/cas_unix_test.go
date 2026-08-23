//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"golang.org/x/sys/unix"
)

func TestCASRejectsFIFOWithoutBlockingAndPutRepairs(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("fifo-repair")
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	shard, _ := blockIdentity(key)
	if err := os.Mkdir(filepath.Join(store.blocks, shard), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(store.blockPath(key), 0o600); err != nil {
		t.Fatal(err)
	}

	getErr := callLocalCASWithin(t, func() error {
		_, err := store.Get(t.Context(), key)
		return err
	})
	if !errors.Is(getErr, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Get FIFO error = %v, want ErrCorruptedBlock", getErr)
	}
	hasErr := callLocalCASWithin(t, func() error {
		_, err := store.Has(t.Context(), key)
		return err
	})
	if !errors.Is(hasErr, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Has FIFO error = %v, want ErrCorruptedBlock", hasErr)
	}
	putErr := callLocalCASWithin(t, func() error {
		_, err := store.Put(t.Context(), body)
		return err
	})
	if putErr != nil {
		t.Fatalf("Put did not atomically repair FIFO: %v", putErr)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("repaired FIFO body = %q, %v", got, err)
	}
}

func TestCASRepairsUnreadableOrNonRegularTargets(t *testing.T) {
	for _, object := range []string{"mode-000", "socket"} {
		t.Run(object, func(t *testing.T) {
			base, err := os.MkdirTemp("", "mcas-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(base) })
			store := openTestCAS(t, Options{Directory: base})
			body := []byte("repair-unreadable-" + object)
			key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
			if err != nil {
				t.Fatal(err)
			}
			shard, _ := blockIdentity(key)
			if err := os.Mkdir(filepath.Join(store.blocks, shard), 0o700); err != nil {
				t.Fatal(err)
			}
			switch object {
			case "mode-000":
				if err := os.WriteFile(store.blockPath(key), body, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(store.blockPath(key), 0); err != nil {
					t.Fatal(err)
				}
			case "socket":
				fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				unix.CloseOnExec(fd)
				defer unix.Close(fd)
				if err := unix.Bind(fd, &unix.SockaddrUnix{Name: store.blockPath(key)}); err != nil {
					t.Fatal(err)
				}
			}

			getErr := callLocalCASWithin(t, func() error {
				_, err := store.Get(t.Context(), key)
				return err
			})
			if !errors.Is(getErr, transportcap.ErrCorruptedBlock) {
				t.Fatalf("Get unsafe target error = %v, want ErrCorruptedBlock", getErr)
			}
			if _, err := store.Put(t.Context(), body); err != nil {
				t.Fatalf("Put did not repair unsafe target: %v", err)
			}
			got, err := store.Get(t.Context(), key)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("repaired target body = %q, %v", got, err)
			}
		})
	}
}

func TestCASPutRetriesUnconfirmedDirectoryDurability(t *testing.T) {
	tests := []struct {
		name       string
		directory  string
		occurrence int
	}{
		{name: "create-shard-parent", directory: "blocks", occurrence: 1},
		{name: "install-target", directory: "shard", occurrence: 1},
		{name: "confirm-shard", directory: "shard", occurrence: 2},
		{name: "confirm-shard-parent", directory: "blocks", occurrence: 2},
		{name: "confirm-blocks-parent", directory: "root", occurrence: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestCAS(t, Options{Directory: t.TempDir()})
			body := []byte("durability-retry-" + test.name)
			key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
			if err != nil {
				t.Fatal(err)
			}
			shard, _ := blockIdentity(key)
			platform := store.platform.(*platformStore)
			realSync := platform.syncDirectory
			failure := errors.New("injected directory sync failure")
			failed := false
			occurrences := map[string]int{}
			platform.syncDirectory = func(directory *os.File) error {
				name := filepath.Base(directory.Name())
				kind := ""
				switch name {
				case "blocks":
					kind = "blocks"
				case shard:
					kind = "shard"
				default:
					kind = "root"
				}
				occurrences[kind]++
				if !failed && kind == test.directory && occurrences[kind] == test.occurrence {
					failed = true
					return failure
				}
				return realSync(directory)
			}

			if _, err := store.Put(t.Context(), body); !errors.Is(err, failure) {
				t.Fatalf("first Put error = %v, want injected sync failure", err)
			}
			if !failed {
				t.Fatal("directory sync failure was not injected")
			}
			if _, err := store.Put(t.Context(), body); err != nil {
				t.Fatalf("retry did not confirm the complete durability chain: %v", err)
			}
			got, err := store.Get(t.Context(), key)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("durable retry body = %q, %v", got, err)
			}
		})
	}
}

func TestCASCloseRetriesOnlyUnconfirmedPlatformComponents(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	platform := store.platform.(*platformStore)
	realClose := platform.closeFile
	failure := errors.New("injected close failure")
	failed := false
	platform.closeFile = func(file *os.File) error {
		if !failed && file == platform.blocks {
			failed = true
			return errors.Join(realClose(file), failure)
		}
		return realClose(file)
	}
	if err := store.Close(); !errors.Is(err, failure) {
		t.Fatalf("first Close error = %v, want injected failure", err)
	}
	if store.platform == nil || platform.blocks != nil || platform.root != nil {
		t.Fatalf("failed Close ownership = store:%T blocks:%v root:%v", store.platform, platform.blocks, platform.root)
	}
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: []byte("closed")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, errCASClosed) {
		t.Fatalf("Get after failed Close = %v, want closed error", err)
	}
	if _, err := store.Has(t.Context(), key); !errors.Is(err, errCASClosed) {
		t.Fatalf("Has after failed Close = %v, want closed error", err)
	}
	if _, err := store.Put(t.Context(), []byte("closed")); !errors.Is(err, errCASClosed) {
		t.Fatalf("Put after failed Close = %v, want closed error", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("retry Close = %v", err)
	}
	if store.platform != nil || platform.blocks != nil || platform.root != nil {
		t.Fatalf("successful retry retained platform ownership")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("idempotent Close = %v", err)
	}
}

func TestOpenRetriesCompleteParentDirectoryDurability(t *testing.T) {
	base := t.TempDir()
	directory := filepath.Join(base, "new", "nested", "cas")
	failedParent := filepath.Join(base, "new", "nested")
	failure := errors.New("injected parent sync failure")
	failed := false
	platform, err := openPlatformStoreWithSync(directory, func(handle *os.File) error {
		if !failed && filepath.Clean(handle.Name()) == failedParent {
			failed = true
			return failure
		}
		return handle.Sync()
	})
	if platform != nil {
		_ = platform.close()
		t.Fatal("Open returned a platform after parent sync failure")
	}
	if !errors.Is(err, failure) {
		t.Fatalf("Open error = %v, want injected parent sync failure", err)
	}
	if !failed {
		t.Fatal("parent sync failure was not injected")
	}

	seen := make(map[string]bool)
	platform, err = openPlatformStoreWithSync(directory, func(handle *os.File) error {
		seen[filepath.Clean(handle.Name())] = true
		return handle.Sync()
	})
	if err != nil {
		t.Fatalf("retry Open = %v", err)
	}
	if !seen[failedParent] || !seen[filepath.Dir(failedParent)] {
		_ = platform.close()
		t.Fatalf("retry sync chain = %#v, want failed parent and its ancestor", seen)
	}
	if err := platform.close(); err != nil {
		t.Fatalf("close retried platform = %v", err)
	}
}

func TestOpenSyncsPinnedParentAfterAncestorPathReplacement(t *testing.T) {
	base := t.TempDir()
	originalTop := filepath.Join(base, "original")
	directory := filepath.Join(originalTop, "nested", "cas")
	movedTop := filepath.Join(base, "moved")
	renamed := false
	var syncedInodes []uint64
	platform, err := openPlatformStoreWithSync(directory, func(handle *os.File) error {
		if filepath.Clean(handle.Name()) == directory && !renamed {
			if err := os.Rename(originalTop, movedTop); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(originalTop, "nested"), 0o700); err != nil {
				return err
			}
			renamed = true
		}
		info, err := handle.Stat()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("unexpected stat type %T", info.Sys())
		}
		syncedInodes = append(syncedInodes, stat.Ino)
		return handle.Sync()
	})
	if err != nil {
		t.Fatalf("Open across ancestor replacement = %v", err)
	}
	defer platform.close()
	if !renamed {
		t.Fatal("ancestor replacement was not injected")
	}
	actual, err := os.Stat(filepath.Join(movedTop, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Stat(filepath.Join(originalTop, "nested"))
	if err != nil {
		t.Fatal(err)
	}
	actualInode := actual.Sys().(*syscall.Stat_t).Ino
	replacementInode := replacement.Sys().(*syscall.Stat_t).Ino
	seenActual := false
	seenReplacement := false
	for _, inode := range syncedInodes {
		seenActual = seenActual || inode == actualInode
		seenReplacement = seenReplacement || inode == replacementInode
	}
	if !seenActual || seenReplacement {
		t.Fatalf("synced inodes = %v, want actual parent %d and not replacement %d", syncedInodes, actualInode, replacementInode)
	}
}

func callLocalCASWithin(t *testing.T, call func() error) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- call() }()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("local CAS call blocked on an untrusted filesystem object")
		return context.DeadlineExceeded
	}
}

func TestCASOpenHandleContainsPostOpenBoundaryReplacement(t *testing.T) {
	for _, component := range []string{"root", "blocks"} {
		t.Run(component, func(t *testing.T) {
			base := t.TempDir()
			directory := filepath.Join(base, "store")
			store := openTestCAS(t, Options{Directory: directory})
			external := filepath.Join(base, "external")
			if err := os.MkdirAll(filepath.Join(external, "blocks"), 0o700); err != nil {
				t.Fatal(err)
			}

			detached := directory + "-detached"
			if component == "blocks" {
				detached = filepath.Join(directory, "blocks-detached")
				if err := os.Rename(filepath.Join(directory, "blocks"), detached); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(external, "blocks"), filepath.Join(directory, "blocks")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(directory, detached); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, directory); err != nil {
					t.Fatal(err)
				}
			}

			body := []byte("descriptor-contained-" + component)
			key, err := store.Put(t.Context(), body)
			if err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(t.Context(), key)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("Get after replacement = %q, %v", got, err)
			}
			shard, name := blockIdentity(key)
			containedPath := filepath.Join(detached, shard, name)
			if component == "root" {
				containedPath = filepath.Join(detached, "blocks", shard, name)
			}
			if _, err := os.Stat(containedPath); err != nil {
				t.Fatalf("block did not remain under opened boundary: %v", err)
			}
			if _, err := os.Stat(filepath.Join(external, "blocks", shard, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("external boundary received a block: %v", err)
			}
			if _, err := Open(Options{Directory: directory}); err == nil {
				t.Fatal("new Open followed the replacement symbolic link")
			}
		})
	}
}

func TestCASRejectsShardSymlinkWithoutWritingOutsideBoundary(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("shard-symlink-attack")
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	shard, name := blockIdentity(key)
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(store.blocks, shard)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(t.Context(), body); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Put through shard symlink error = %v, want ErrCorruptedBlock", err)
	}
	if _, err := os.Stat(filepath.Join(external, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shard symlink escaped store boundary: %v", err)
	}
}

func TestCASReplacesUnsafeTargetWithoutChangingExternalInode(t *testing.T) {
	for _, attack := range []string{"symlink", "hardlink"} {
		t.Run(attack, func(t *testing.T) {
			store := openTestCAS(t, Options{Directory: t.TempDir()})
			body := []byte("safe-target-replacement-" + attack)
			key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
			if err != nil {
				t.Fatal(err)
			}
			shard, _ := blockIdentity(key)
			if err := os.Mkdir(filepath.Join(store.blocks, shard), 0o700); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(t.TempDir(), "external")
			externalBody := []byte("external-must-not-change")
			if attack == "hardlink" {
				externalBody = body
			}
			if err := os.WriteFile(external, externalBody, 0o644); err != nil {
				t.Fatal(err)
			}
			target := store.blockPath(key)
			if attack == "symlink" {
				err = os.Symlink(external, target)
			} else {
				err = os.Link(external, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Put(t.Context(), body); err != nil {
				t.Fatal(err)
			}
			unchanged, err := os.ReadFile(external)
			if err != nil || !bytes.Equal(unchanged, externalBody) {
				t.Fatalf("external inode = %q, %v", unchanged, err)
			}
			info, err := os.Stat(external)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Fatalf("external inode permissions = %#o, want 0644", got)
			}
			got, err := store.Get(t.Context(), key)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("installed block = %q, %v", got, err)
			}
		})
	}
}

func TestCASVerifiedReadRejectsAndPutRepairsUnsafeBlockPermissions(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	key, err := store.Put(t.Context(), []byte("permission-restoration"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(store.blockPath(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Get unsafe block error = %v, want ErrCorruptedBlock", err)
	}
	if _, err := store.Has(t.Context(), key); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Has unsafe block error = %v, want ErrCorruptedBlock", err)
	}
	if _, err := store.Put(t.Context(), []byte("permission-restoration")); err != nil {
		t.Fatalf("Put did not atomically repair unsafe block: %v", err)
	}
	info, err := os.Stat(store.blockPath(key))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("repaired block permissions = %#o, want 0600", got)
	}
}

func TestCASVerifiedReadDoesNotMutateUnsafeShardPermissions(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("shard-permission-repair")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	shard := filepath.Dir(store.blockPath(key))
	if err := os.Chmod(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Get unsafe shard error = %v, want ErrCorruptedBlock", err)
	}
	info, err := os.Stat(shard)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("verified read mutated shard permissions to %#o", got)
	}
	if _, err := store.Put(t.Context(), body); err != nil {
		t.Fatalf("Put did not repair unsafe shard: %v", err)
	}
	info, err = os.Stat(shard)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("repaired shard permissions = %#o, want 0700", got)
	}
}

func TestCASUnreadableShardRequiresExplicitOfflineRepair(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("unreadable-shard")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	shard := filepath.Dir(store.blockPath(key))
	if err := os.Chmod(shard, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Get unreadable shard error = %v, want ErrCorruptedBlock", err)
	}
	info, err := os.Stat(shard)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0 {
		t.Fatalf("verified read mutated unreadable shard permissions to %#o", got)
	}
	if _, err := store.Put(t.Context(), body); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("Put unreadable shard error = %v, want safe refusal", err)
	}
	info, err = os.Stat(shard)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0 {
		t.Fatalf("Put mutated unreadable shard permissions to %#o", got)
	}
	if err := os.Chmod(shard, 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Get after explicit shard repair = %q, %v", got, err)
	}
}
