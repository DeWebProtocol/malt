//go:build windows

package local

import (
	"bytes"
	"errors"
	"os"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/transport/capability"
	"golang.org/x/sys/windows"
)

func TestCASAtomicReplacementWhileTargetReadHandleIsOpen(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("windows-delete-sharing")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	handle, _, err := openWindowsFile(store.blockPath(key), windows.GENERIC_READ, windows.OPEN_EXISTING, true)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)

	shard, name := blockIdentity(key)
	if err := store.platform.writeBlock(t.Context(), shard, name, body); err != nil {
		t.Fatalf("atomic replacement while reader holds target: %v", err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Get after replacement = %q, %v", got, err)
	}
}

func TestCASRepairsDenyReadBlockDACL(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("windows-deny-read-repair")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(D;;GR;;;OW)(A;;GA;;;SY)(A;;RCWDACWO;;;OW)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		store.blockPath(key), windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, capability.ErrCorruptedBlock) {
		t.Fatalf("Get deny-read block error = %v, want ErrCorruptedBlock", err)
	}
	if _, err := store.Put(t.Context(), body); err != nil {
		t.Fatalf("Put did not repair deny-read DACL: %v", err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Get repaired block = %q, %v", got, err)
	}
}

func TestCASCloseRetriesOnlyUnconfirmedWindowsComponents(t *testing.T) {
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
		t.Fatalf("failed Close retained an invalid terminally closed component")
	}
	key, err := clientcas.CIDForBlock(capability.Block{Data: []byte("closed")})
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
	if store.platform != nil {
		t.Fatal("successful retry retained platform ownership")
	}
}

func TestCASPreservesOperationalShardOpenErrors(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("windows-operational-shard-error")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	platform := store.platform.(*platformStore)
	platform.openDirectory = func(string) (*os.File, error) {
		return nil, windows.ERROR_SHARING_VIOLATION
	}
	_, err = store.Get(t.Context(), key)
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("Get operational shard error = %v, want sharing violation", err)
	}
	if errors.Is(err, capability.ErrCorruptedBlock) {
		t.Fatalf("Get operational shard error = %v, must not be corruption", err)
	}
}
