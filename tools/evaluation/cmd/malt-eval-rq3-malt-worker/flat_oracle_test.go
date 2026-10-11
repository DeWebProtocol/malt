package main

import (
	"fmt"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	cid "github.com/ipfs/go-cid"
)

func TestFullFlatRootAcceptsStateBeyondSnapshotRequestLimit(t *testing.T) {
	payload, err := clientcas.CIDForBlock(clientcas.Block{Codec: cid.Raw, Data: []byte("retained payload")})
	if err != nil {
		t.Fatal(err)
	}
	// Model live disk-backed source metadata: the full oracle must use each
	// retained payload CID, without loading file bodies or using Gateway state.
	file := logicalFile{payload: payload, mode: 0o644}
	files := make(map[string]logicalFile, maximumMALTSnapshotFiles+1)
	var lastPath string
	for i := 0; i <= maximumMALTSnapshotFiles; i++ {
		lastPath = fmt.Sprintf("src/file-%06d.txt", i)
		files[lastPath] = file
	}
	if _, err := directFlatSnapshotChanges(files); err == nil {
		t.Fatal("oversized Gateway snapshot request was accepted")
	}
	changes, err := flatSnapshotChanges(files)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(changes), 1+2*len(files); got != want {
		t.Fatalf("full projection has %d bindings; want %d", got, want)
	}
	full, err := fullFlatRoot(t.Context(), files)
	if err != nil {
		t.Fatalf("full oracle for %d live files: %v", len(files), err)
	}
	if !full.Defined() {
		t.Fatal("full oracle returned an undefined Root")
	}

	// The exact request boundary still accepts a complete initial snapshot.
	// Incremental/full Root agreement is covered by the small-state oracle test;
	// avoid a second large KZG construction in the default test suite.
	delete(files, lastPath)
	initial, err := directFlatSnapshotChanges(files)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(initial), 1+2*len(files); got != want {
		t.Fatalf("request projection has %d bindings; want %d", got, want)
	}
}
