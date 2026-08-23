package memory

import (
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/cas"
	cid "github.com/ipfs/go-cid"
)

func TestGetClassifiesMissingBlock(t *testing.T) {
	missing, err := cas.CIDForBlock(cas.Block{Data: []byte("missing"), Codec: cid.Raw})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New().Get(t.Context(), missing)
	if !errors.Is(err, cas.ErrNotFound) {
		t.Fatalf("Get error = %v, want cas.ErrNotFound", err)
	}
	if _, err := New().Get(t.Context(), cid.Undef); !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("undefined Get error = %v, want cas.ErrCorruptedBlock", err)
	}
}
