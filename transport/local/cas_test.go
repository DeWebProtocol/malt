package local

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

func TestBlockIdentityShardsByDigestByte(t *testing.T) {
	for _, body := range [][]byte{[]byte("a"), []byte("b")} {
		key, err := clientcas.CIDForBlock(transportcap.Block{Data: body, Codec: cid.Raw})
		if err != nil {
			t.Fatal(err)
		}
		shard, _ := blockIdentity(key)
		digest := sha256.Sum256(body)
		if want := fmt.Sprintf("%02x", digest[0]); shard != want {
			t.Fatalf("blockIdentity(%q) shard = %q, want digest shard %q", body, shard, want)
		}
	}
}

func TestCASPersistsAcrossOpenAndProtectsState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "local-cas")
	store := openTestCAS(t, Options{Directory: directory})
	body := []byte("persistent local block")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	reopened := openTestCAS(t, Options{Directory: directory})
	got, err := reopened.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("reopened Get = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{directory, filepath.Join(directory, "blocks")} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("%s permissions = %#o, want 0700", path, got)
			}
		}
		info, err := os.Stat(store.blockPath(key))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("block permissions = %#o, want 0600", got)
		}
	}
}

func TestCASRejectsCorruptionAndPutRepairsExactBlock(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	body := []byte("repairable local block")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.blockPath(key), []byte("substituted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("corrupt Get error = %v, want ErrCorruptedBlock", err)
	}
	if _, err := store.Has(t.Context(), key); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("corrupt Has error = %v, want ErrCorruptedBlock", err)
	}
	repaired, err := store.Put(t.Context(), body)
	if err != nil || !repaired.Equals(key) {
		t.Fatalf("repair Put = %s, %v", repaired, err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("repaired Get = %q, %v", got, err)
	}
}

func TestCASBatchPreflightAndDuplicateIdentity(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir(), MaxBlockBytes: 8, MaxBatchBytes: 8, MaxBatchBlocks: 3})
	if _, err := store.PutBatch(t.Context(), []transportcap.Block{{Data: []byte("small")}, {Data: []byte("too-large")}}); err == nil {
		t.Fatal("oversized batch succeeded")
	}
	small, err := clientcas.CIDForBlock(transportcap.Block{Data: []byte("small")})
	if err != nil {
		t.Fatal(err)
	}
	present, err := store.Has(t.Context(), small)
	if err != nil || present {
		t.Fatalf("preflight wrote first block: present=%v err=%v", present, err)
	}
	results, err := store.PutBatch(t.Context(), []transportcap.Block{{Data: []byte("same")}, {Data: []byte("same")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || !results[0].CID.Equals(results[1].CID) || results[1].Status != transportcap.PutStatusDuplicateInRequest {
		t.Fatalf("duplicate results = %#v", results)
	}
}

func TestCASBatchErrorMayLeaveVerifiedSubsetAndWholeBatchRetryIsSafe(t *testing.T) {
	store := openTestCAS(t, Options{Directory: t.TempDir()})
	ctx, cancel := context.WithCancel(t.Context())
	store.platform = &cancelAfterWriteStore{inner: store.platform, cancel: cancel}
	blocks := []transportcap.Block{{Data: []byte("persisted-before-cancel")}, {Data: []byte("not-yet-persisted")}}
	results, err := store.PutBatch(ctx, blocks)
	if !errors.Is(err, context.Canceled) || results != nil {
		t.Fatalf("canceled PutBatch = %#v, %v; want nil, context.Canceled", results, err)
	}
	first, err := clientcas.CIDForBlock(blocks[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := clientcas.CIDForBlock(blocks[1])
	if err != nil {
		t.Fatal(err)
	}
	present, err := store.HasBatch(t.Context(), []cid.Cid{first, second})
	if err != nil || len(present) != 2 || !present[0] || present[1] {
		t.Fatalf("partial persistence = %#v, %v; want [true false]", present, err)
	}
	retried, err := store.PutBatch(t.Context(), blocks)
	if err != nil || len(retried) != 2 || !retried[0].CID.Equals(first) || !retried[1].CID.Equals(second) {
		t.Fatalf("whole-batch retry = %#v, %v", retried, err)
	}
}

func TestCASConcurrentImmutableWrites(t *testing.T) {
	directory := t.TempDir()
	first := openTestCAS(t, Options{Directory: directory})
	second := openTestCAS(t, Options{Directory: directory})
	body := bytes.Repeat([]byte("concurrent"), 1024)
	want, err := clientcas.CIDForBlock(transportcap.Block{Data: body, Codec: cid.Raw})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsOut := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			store := first
			if index%2 != 0 {
				store = second
			}
			got, err := store.Put(context.Background(), body)
			if err == nil && !got.Equals(want) {
				err = errors.New("concurrent Put returned the wrong CID")
			}
			errorsOut <- err
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := first.Get(t.Context(), want)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("concurrent final Get = %d bytes, %v", len(got), err)
	}
}

func TestOpenRejectsUnsafeOrInvalidBoundaries(t *testing.T) {
	if _, err := Open(Options{}); err == nil {
		t.Fatal("empty local CAS directory succeeded")
	}
	root := filepath.VolumeName(string(filepath.Separator)) + string(filepath.Separator)
	if _, err := Open(Options{Directory: root}); err == nil {
		t.Fatal("filesystem-root local CAS succeeded")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Options{Directory: file}); err == nil {
		t.Fatal("file local CAS boundary succeeded")
	}
}

type cancelAfterWriteStore struct {
	inner  blockStore
	cancel context.CancelFunc
	writes int
}

func openTestCAS(t *testing.T, options Options) *CAS {
	t.Helper()
	store, err := Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close local CAS: %v", err)
		}
	})
	return store
}

func (s *cancelAfterWriteStore) readBlock(ctx context.Context, shard, name string, maxBytes int64) ([]byte, error) {
	return s.inner.readBlock(ctx, shard, name, maxBytes)
}

func (s *cancelAfterWriteStore) writeBlock(ctx context.Context, shard, name string, data []byte) error {
	if err := s.inner.writeBlock(ctx, shard, name, data); err != nil {
		return err
	}
	s.writes++
	if s.writes == 1 {
		s.cancel()
	}
	return nil
}

func (s *cancelAfterWriteStore) ensureDurable(ctx context.Context, shard string) error {
	return s.inner.ensureDurable(ctx, shard)
}

func (s *cancelAfterWriteStore) close() error {
	return s.inner.close()
}
