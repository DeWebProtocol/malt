package cas

import (
	"context"
	"errors"
	"fmt"

	cid "github.com/ipfs/go-cid"
)

// ErrCorruptedBlock is returned when a CAS Get returns bytes whose multihash
// does not match the requested CID. The MALT trust model treats CAS as
// untrusted execution state (see ARCHITECTURE.md, section "Trust Model"); a
// reader that does not verify hashes lets a compromised CAS substitute
// arbitrary content underneath ProofList header guarantees.
var ErrCorruptedBlock = errors.New("cas: returned block does not match requested CID")

// VerifyingReader wraps a CAS Reader and validates that bytes returned by Get
// hash to the requested CID. It also forwards Has unchanged and, if the
// underlying reader implements BatchReader, exposes HasBatch.
//
// The verification is intentionally cheap: it reuses the multihash carried in
// the requested CID, recomputes it over the returned bytes, and rejects on
// mismatch. Callers that want to skip verification (for example for
// non-content-addressed identifiers) must call the underlying reader
// directly.
type VerifyingReader struct {
	inner Reader
}

// NewVerifyingReader wraps r so that Get verifies returned bytes against the
// requested CID. A nil inner reader is treated as a programming error.
func NewVerifyingReader(r Reader) *VerifyingReader {
	if r == nil {
		panic("cas: NewVerifyingReader called with nil reader")
	}
	return &VerifyingReader{inner: r}
}

// Inner returns the wrapped reader, useful for tests and adapters that need
// to bypass verification deliberately.
func (v *VerifyingReader) Inner() Reader {
	return v.inner
}

// Get returns the bytes for c only if their multihash matches c. Returned
// errors from the underlying reader are propagated unchanged; verification
// failures are wrapped with ErrCorruptedBlock so callers can identify them
// with errors.Is.
func (v *VerifyingReader) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	if !c.Defined() {
		// Refuse to issue a Get for an undefined CID at all. There is no
		// hash to validate against, and any returned bytes would be
		// indistinguishable from forged content.
		return nil, fmt.Errorf("%w: undefined CID", ErrCorruptedBlock)
	}
	data, err := v.inner.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	got, err := cidForData(c, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptedBlock, err)
	}
	if !got.Equals(c) {
		return nil, fmt.Errorf("%w: got %s want %s", ErrCorruptedBlock, got.String(), c.String())
	}
	return data, nil
}

// Has forwards to the underlying reader.
func (v *VerifyingReader) Has(ctx context.Context, c cid.Cid) (bool, error) {
	return v.inner.Has(ctx, c)
}

// HasBatch forwards when the underlying reader supports it. Implementations
// that do not implement BatchReader fall through to per-CID Has checks via
// the cas package contract elsewhere; here we only forward the optimization.
func (v *VerifyingReader) HasBatch(ctx context.Context, cids []cid.Cid) ([]bool, error) {
	if br, ok := v.inner.(BatchReader); ok {
		return br.HasBatch(ctx, cids)
	}
	results := make([]bool, len(cids))
	for i, c := range cids {
		ok, err := v.inner.Has(ctx, c)
		if err != nil {
			return nil, err
		}
		results[i] = ok
	}
	return results, nil
}

// errReadOnlyCAS is returned when a write method is called against a wrapper
// whose inner reader does not support that write surface. Tests inject
// pure-reader CAS implementations (mock.shortReader, etc.); we expose the
// error so callers can distinguish "not configured" from "rejected".
var errReadOnlyCAS = errors.New("cas: underlying reader does not support writes")

// Put forwards to the inner reader if it implements Writer. The verifying
// wrapper does not gate writes because content addressing already guarantees
// integrity: the writer derives the CID from the bytes it just stored.
func (v *VerifyingReader) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	w, ok := v.inner.(Writer)
	if !ok {
		return cid.Undef, errReadOnlyCAS
	}
	return w.Put(ctx, data)
}

// PutWithCodec forwards to the inner reader if it implements TypedWriter.
// Falls back to Put for cid.Raw when typed writes are unavailable.
func (v *VerifyingReader) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if tw, ok := v.inner.(TypedWriter); ok {
		return tw.PutWithCodec(ctx, data, codec)
	}
	if NormalizeCodec(codec) == cid.Raw {
		return v.Put(ctx, data)
	}
	return cid.Undef, errReadOnlyCAS
}

// PutBatch forwards to the inner reader if it implements BatchWriter.
// Otherwise falls back to per-block PutWithCodec via the cas package's
// PutBlocks helper, exactly as cas.PutBlocks does for non-batch writers.
//
// The fallback dispatches against the inner Writer (not the wrapper itself)
// because PutBlocks routes BatchWriter implementations through their own
// PutBatch; passing v back into PutBlocks would recurse forever.
func (v *VerifyingReader) PutBatch(ctx context.Context, blocks []Block) ([]PutResult, error) {
	if bw, ok := v.inner.(BatchWriter); ok {
		return bw.PutBatch(ctx, blocks)
	}
	w, ok := v.inner.(Writer)
	if !ok {
		return nil, errReadOnlyCAS
	}
	return PutBlocks(ctx, w, blocks)
}

// SnapshotStats forwards to the inner reader if it provides a metrics
// snapshot. This keeps the metrics pipeline transparent through the wrapper.
func (v *VerifyingReader) SnapshotStats() Stats {
	if s, ok := v.inner.(interface{ SnapshotStats() Stats }); ok {
		return s.SnapshotStats()
	}
	return Stats{}
}

// ResetStats forwards to the inner reader when supported.
func (v *VerifyingReader) ResetStats() {
	if r, ok := v.inner.(interface{ ResetStats() }); ok {
		r.ResetStats()
	}
}

// cidForData recomputes the CID for data using the same codec and multihash
// algorithm as the requested CID. Reusing the requested CID's prefix keeps
// the comparison meaningful even for non-default codecs (DAG-CBOR, DAG-JSON,
// etc.).
func cidForData(want cid.Cid, data []byte) (cid.Cid, error) {
	prefix := want.Prefix()
	return prefix.Sum(data)
}
