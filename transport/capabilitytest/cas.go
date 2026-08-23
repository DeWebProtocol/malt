// Package capabilitytest provides reusable semantic transport conformance
// suites. Gateway, local, peer, and hybrid adapters can run the same tests
// without exposing their routes or backend DTOs to applications.
package capabilitytest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

// CASFactory returns a fresh isolated CAS for one contract case.
type CASFactory func(*testing.T) transportcap.CAS

// RunCAS runs the transport-neutral immutable-byte contract. Implementations
// that also expose BatchCAS receive the ordered batch extension cases.
func RunCAS(t *testing.T, factory CASFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("CAS contract factory is nil")
	}

	t.Run("raw-round-trip", func(t *testing.T) {
		store := requireCAS(t, factory)
		original := []byte("capability-contract-raw")
		input := append([]byte(nil), original...)
		want := canonicalCID(t, original, cid.Raw)
		key, err := store.Put(t.Context(), input)
		if err != nil {
			t.Fatal(err)
		}
		if !key.Equals(want) {
			t.Fatalf("Put CID = %s, want %s", key, want)
		}
		input[0] ^= 0xff
		present, err := store.Has(t.Context(), key)
		if err != nil || !present {
			t.Fatalf("Has = %v, %v", present, err)
		}
		body, err := store.Get(t.Context(), key)
		if err != nil || !bytes.Equal(body, original) {
			t.Fatalf("Get = %q, %v", body, err)
		}
		body[0] ^= 0xff
		again, err := store.Get(t.Context(), key)
		if err != nil || !bytes.Equal(again, original) {
			t.Fatalf("second Get = %q, %v", again, err)
		}
		repeated, err := store.Put(t.Context(), original)
		if err != nil || !repeated.Equals(key) {
			t.Fatalf("idempotent Put = %s, %v", repeated, err)
		}
	})

	t.Run("typed-round-trip", func(t *testing.T) {
		store := requireCAS(t, factory)
		body := []byte{0xa1, 0x61, 0x61, 0x01}
		const dagCBOR = uint64(0x71)
		want := canonicalCID(t, body, dagCBOR)
		key, err := store.PutWithCodec(t.Context(), body, dagCBOR)
		if err != nil || !key.Equals(want) {
			t.Fatalf("typed Put = %s, %v; want %s", key, err, want)
		}
		got, err := store.Get(t.Context(), key)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("typed Get = %x, %v", got, err)
		}
		raw, err := store.PutWithCodec(t.Context(), body, 0)
		if err != nil || !raw.Equals(canonicalCID(t, body, cid.Raw)) {
			t.Fatalf("zero-codec Put = %s, %v", raw, err)
		}
	})

	t.Run("missing-and-undefined", func(t *testing.T) {
		store := requireCAS(t, factory)
		missing := canonicalCID(t, []byte("capability-contract-missing"), cid.Raw)
		present, err := store.Has(t.Context(), missing)
		if err != nil || present {
			t.Fatalf("missing Has = %v, %v", present, err)
		}
		if _, err := store.Get(t.Context(), missing); !errors.Is(err, transportcap.ErrNotFound) {
			t.Fatalf("missing Get error = %v, want ErrNotFound", err)
		}
		if _, err := store.Get(t.Context(), cid.Undef); !errors.Is(err, transportcap.ErrCorruptedBlock) {
			t.Fatalf("undefined Get error = %v, want ErrCorruptedBlock", err)
		}
		if _, err := store.Has(t.Context(), cid.Undef); !errors.Is(err, transportcap.ErrCorruptedBlock) {
			t.Fatalf("undefined Has error = %v, want ErrCorruptedBlock", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		store := requireCAS(t, factory)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		key := canonicalCID(t, []byte("capability-contract-cancel"), cid.Raw)
		for name, call := range map[string]func() error{
			"put":       func() error { _, err := store.Put(ctx, []byte("cancel")); return err },
			"put-typed": func() error { _, err := store.PutWithCodec(ctx, []byte("cancel"), cid.Raw); return err },
			"get":       func() error { _, err := store.Get(ctx, key); return err },
			"has":       func() error { _, err := store.Has(ctx, key); return err },
		} {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Errorf("%s error = %v, want context.Canceled", name, err)
			}
		}
	})

	t.Run("ordered-batch-extension", func(t *testing.T) {
		store := requireCAS(t, factory)
		batch, ok := store.(transportcap.BatchCAS)
		if !ok {
			t.Skip("transport does not expose the optional ordered batch extension")
		}
		if results, err := batch.PutBatch(t.Context(), nil); err != nil || len(results) != 0 {
			t.Fatalf("empty PutBatch = %#v, %v", results, err)
		}
		if present, err := batch.HasBatch(t.Context(), nil); err != nil || len(present) != 0 {
			t.Fatalf("empty HasBatch = %#v, %v", present, err)
		}
		blocks := []transportcap.Block{
			{Data: []byte("batch-raw"), Codec: cid.Raw},
			{Data: []byte{0xa1, 0x61, 0x62, 0x02}, Codec: 0x71},
			{Data: []byte("batch-raw"), Codec: cid.Raw},
		}
		results, err := batch.PutBatch(t.Context(), blocks)
		if err != nil || len(results) != len(blocks) {
			t.Fatalf("PutBatch = %#v, %v", results, err)
		}
		for index, block := range blocks {
			want := canonicalCID(t, block.Data, block.Codec)
			if !results[index].CID.Equals(want) {
				t.Errorf("PutBatch[%d] CID = %s, want %s", index, results[index].CID, want)
			}
		}
		missing := canonicalCID(t, []byte("batch-missing"), cid.Raw)
		present, err := batch.HasBatch(t.Context(), []cid.Cid{results[1].CID, missing, results[0].CID})
		if err != nil || len(present) != 3 || !present[0] || present[1] || !present[2] {
			t.Fatalf("ordered HasBatch = %#v, %v", present, err)
		}
	})
}

func requireCAS(t *testing.T, factory CASFactory) transportcap.CAS {
	t.Helper()
	store := factory(t)
	if store == nil {
		t.Fatal("CAS contract factory returned nil")
	}
	return store
}

func canonicalCID(t *testing.T, body []byte, codec uint64) cid.Cid {
	t.Helper()
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: body, Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	return key
}
