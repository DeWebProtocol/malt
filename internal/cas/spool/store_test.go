package spool

import (
	"os"
	"path/filepath"
	"testing"

	cid "github.com/ipfs/go-cid"
)

func TestStoreRoundTripAndCorruptionRejection(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("encrypted snapshot block")
	key, err := store.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || key.Type() != cid.Raw {
		t.Fatalf("snapshot block = %q CID=%s", got, key)
	}
	if err := os.WriteFile(filepath.Join(store.directory, "blocks", key.String()), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), key); err == nil {
		t.Fatal("corrupt snapshot block was accepted")
	}
}
