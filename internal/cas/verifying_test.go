package cas

import (
	"context"
	"errors"
	"testing"

	cid "github.com/ipfs/go-cid"
)

type malformedVerifyingCAS struct {
	hasCalls   int
	hasBatch   []bool
	putResults []PutResult
}

func (c *malformedVerifyingCAS) Get(context.Context, cid.Cid) ([]byte, error) {
	return nil, errors.New("unexpected Get")
}

func (c *malformedVerifyingCAS) Has(context.Context, cid.Cid) (bool, error) {
	c.hasCalls++
	return true, nil
}

func (c *malformedVerifyingCAS) HasBatch(context.Context, []cid.Cid) ([]bool, error) {
	c.hasCalls++
	return append([]bool(nil), c.hasBatch...), nil
}

func (c *malformedVerifyingCAS) Put(context.Context, []byte) (cid.Cid, error) {
	return cid.Undef, errors.New("unexpected Put")
}

func (c *malformedVerifyingCAS) PutBatch(context.Context, []Block) ([]PutResult, error) {
	return append([]PutResult(nil), c.putResults...), nil
}

func TestVerifyingReaderRejectsUndefinedHasIdentitiesBeforeDispatch(t *testing.T) {
	inner := &malformedVerifyingCAS{}
	verified := NewVerifyingReader(inner)
	if _, err := verified.Has(t.Context(), cid.Undef); !errors.Is(err, ErrCorruptedBlock) {
		t.Fatalf("Has undefined CID error = %v, want ErrCorruptedBlock", err)
	}
	if _, err := verified.HasBatch(t.Context(), []cid.Cid{cid.Undef}); !errors.Is(err, ErrCorruptedBlock) {
		t.Fatalf("HasBatch undefined CID error = %v, want ErrCorruptedBlock", err)
	}
	if inner.hasCalls != 0 {
		t.Fatalf("undefined identities reached untrusted reader %d times", inner.hasCalls)
	}
}

func TestVerifyingReaderRejectsMalformedHasBatchLength(t *testing.T) {
	body := []byte("has-batch-identity")
	key, err := CIDForBlock(Block{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	inner := &malformedVerifyingCAS{hasBatch: []bool{}}
	if _, err := NewVerifyingReader(inner).HasBatch(t.Context(), []cid.Cid{key}); !errors.Is(err, ErrCorruptedBlock) {
		t.Fatalf("HasBatch wrong length error = %v, want ErrCorruptedBlock", err)
	}
}

func TestVerifyingReaderRejectsUnknownPutBatchStatus(t *testing.T) {
	body := []byte("put-batch-receipt")
	key, err := CIDForBlock(Block{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	inner := &malformedVerifyingCAS{putResults: []PutResult{{CID: key, Status: "invented"}}}
	if _, err := NewVerifyingReader(inner).PutBatch(t.Context(), []Block{{Data: body}}); !errors.Is(err, ErrCorruptedBlock) {
		t.Fatalf("PutBatch unknown status error = %v, want ErrCorruptedBlock", err)
	}
}
