package unixfs

import (
	"strings"
	"testing"

	cid "github.com/ipfs/go-cid"
)

func TestDirectoryManifestPayloadEntriesRejectsNilCASReader(t *testing.T) {
	_, err := DirectoryManifestPayloadEntries(t.Context(), nil, cid.Undef)
	if err == nil || !strings.Contains(err.Error(), "CAS reader is nil") {
		t.Fatalf("DirectoryManifestPayloadEntries error = %v, want nil CAS reader diagnostic", err)
	}
}

func TestManifestDirectoryEntriesRecognizesManifestBeforeNilCASDiagnostic(t *testing.T) {
	manifestCID, err := NewDirectoryManifestCID(nil)
	if err != nil {
		t.Fatalf("NewDirectoryManifestCID: %v", err)
	}
	_, recognized, err := ManifestDirectoryEntries(t.Context(), nil, manifestCID)
	if !recognized {
		t.Fatal("ManifestDirectoryEntries did not recognize manifest CID")
	}
	if err == nil || !strings.Contains(err.Error(), "CAS reader is nil") {
		t.Fatalf("ManifestDirectoryEntries error = %v, want nil CAS reader diagnostic", err)
	}
}
