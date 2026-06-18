package cas_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt/storage/cas"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// fixedReader is a minimal cas.Reader that returns canned bytes for a
// requested CID. It lets tests cover both the happy and tampered paths
// without standing up the IPFS-backed mock.
type fixedReader struct {
	get      map[string][]byte
	has      map[string]bool
	getErr   error
	hasErr   error
	getCalls int
	hasCalls int
}

func (r *fixedReader) Get(_ context.Context, c cid.Cid) ([]byte, error) {
	r.getCalls++
	if r.getErr != nil {
		return nil, r.getErr
	}
	data, ok := r.get[c.String()]
	if !ok {
		return nil, errors.New("not found")
	}
	return data, nil
}

func (r *fixedReader) Has(_ context.Context, c cid.Cid) (bool, error) {
	r.hasCalls++
	if r.hasErr != nil {
		return false, r.hasErr
	}
	return r.has[c.String()], nil
}

// batchedFixedReader extends fixedReader with a HasBatch implementation so we
// can exercise the wrapper's BatchReader pass-through.
type batchedFixedReader struct {
	*fixedReader
	batchCalls int
}

func (r *batchedFixedReader) HasBatch(_ context.Context, cids []cid.Cid) ([]bool, error) {
	r.batchCalls++
	out := make([]bool, len(cids))
	for i, c := range cids {
		out[i] = r.has[c.String()]
	}
	return out, nil
}

// statsReader exposes SnapshotStats/ResetStats so tests can confirm the
// wrapper forwards those via interface assertions.
type statsReader struct {
	*fixedReader
	snap     cas.Stats
	resetHit int
}

func (s *statsReader) SnapshotStats() cas.Stats { return s.snap }
func (s *statsReader) ResetStats()              { s.resetHit++ }

func cidForRaw(t *testing.T, data []byte) cid.Cid {
	t.Helper()
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("multihash sum: %v", err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

func TestVerifyingReader_GetMatchingHash(t *testing.T) {
	data := []byte("the quick brown fox")
	c := cidForRaw(t, data)
	inner := &fixedReader{get: map[string][]byte{c.String(): data}}
	v := cas.NewVerifyingReader(inner)

	got, err := v.Get(context.Background(), c)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("Get returned %q, want %q", got, data)
	}
	if inner.getCalls != 1 {
		t.Fatalf("inner Get called %d times, want 1", inner.getCalls)
	}
}

func TestVerifyingReader_GetTamperedBytes(t *testing.T) {
	data := []byte("trusted contents")
	c := cidForRaw(t, data)
	inner := &fixedReader{get: map[string][]byte{c.String(): []byte("evil bytes")}}
	v := cas.NewVerifyingReader(inner)

	_, err := v.Get(context.Background(), c)
	if err == nil {
		t.Fatal("expected verification error, got nil")
	}
	if !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("error %v is not ErrCorruptedBlock", err)
	}
}

func TestVerifyingReader_GetUndefinedCID(t *testing.T) {
	inner := &fixedReader{}
	v := cas.NewVerifyingReader(inner)

	_, err := v.Get(context.Background(), cid.Undef)
	if err == nil {
		t.Fatal("expected error for undefined CID")
	}
	if !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("error %v is not ErrCorruptedBlock", err)
	}
	if inner.getCalls != 0 {
		t.Fatalf("inner Get called %d times for undefined CID, want 0", inner.getCalls)
	}
}

func TestVerifyingReader_GetUnderlyingError(t *testing.T) {
	want := errors.New("transport blew up")
	inner := &fixedReader{getErr: want}
	v := cas.NewVerifyingReader(inner)

	c := cidForRaw(t, []byte("x"))
	_, err := v.Get(context.Background(), c)
	if !errors.Is(err, want) {
		t.Fatalf("Get returned %v, want underlying %v", err, want)
	}
	if errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatal("transport error should not be classified as corrupted")
	}
}

func TestVerifyingReader_HasForwarded(t *testing.T) {
	data := []byte("present")
	c := cidForRaw(t, data)
	inner := &fixedReader{has: map[string]bool{c.String(): true}}
	v := cas.NewVerifyingReader(inner)

	ok, err := v.Has(context.Background(), c)
	if err != nil {
		t.Fatalf("Has returned error: %v", err)
	}
	if !ok {
		t.Fatal("Has returned false for known block")
	}
	if inner.hasCalls != 1 {
		t.Fatalf("inner Has calls = %d, want 1", inner.hasCalls)
	}
}

func TestVerifyingReader_HasBatch_ForwardsWhenSupported(t *testing.T) {
	data := []byte("a")
	c := cidForRaw(t, data)
	inner := &batchedFixedReader{
		fixedReader: &fixedReader{has: map[string]bool{c.String(): true}},
	}
	v := cas.NewVerifyingReader(inner)

	results, err := v.HasBatch(context.Background(), []cid.Cid{c})
	if err != nil {
		t.Fatalf("HasBatch returned error: %v", err)
	}
	if len(results) != 1 || !results[0] {
		t.Fatalf("HasBatch results = %v, want [true]", results)
	}
	if inner.batchCalls != 1 {
		t.Fatalf("inner batchCalls = %d, want 1", inner.batchCalls)
	}
	if inner.hasCalls != 0 {
		t.Fatalf("inner hasCalls = %d, want 0 (BatchReader path)", inner.hasCalls)
	}
}

func TestVerifyingReader_HasBatch_FallbackToHas(t *testing.T) {
	data := []byte("b")
	c := cidForRaw(t, data)
	missing := cidForRaw(t, []byte("missing"))
	inner := &fixedReader{
		has: map[string]bool{c.String(): true},
	}
	v := cas.NewVerifyingReader(inner)

	results, err := v.HasBatch(context.Background(), []cid.Cid{c, missing})
	if err != nil {
		t.Fatalf("HasBatch returned error: %v", err)
	}
	if len(results) != 2 || !results[0] || results[1] {
		t.Fatalf("HasBatch results = %v, want [true,false]", results)
	}
	if inner.hasCalls != 2 {
		t.Fatalf("inner Has calls = %d, want 2", inner.hasCalls)
	}
}

func TestVerifyingReader_StatsForwarded(t *testing.T) {
	inner := &statsReader{
		fixedReader: &fixedReader{},
		snap:        cas.Stats{GetCount: 7, BytesGet: 99},
	}
	v := cas.NewVerifyingReader(inner)
	if got := v.SnapshotStats(); got != inner.snap {
		t.Fatalf("SnapshotStats = %+v, want %+v", got, inner.snap)
	}
	v.ResetStats()
	if inner.resetHit != 1 {
		t.Fatalf("ResetStats forwarded %d times, want 1", inner.resetHit)
	}
}

func TestVerifyingReader_StatsAbsentReader(t *testing.T) {
	v := cas.NewVerifyingReader(&fixedReader{})
	// Should not panic and should return a zero snapshot.
	if got := v.SnapshotStats(); got != (cas.Stats{}) {
		t.Fatalf("SnapshotStats on plain reader = %+v, want zero", got)
	}
	v.ResetStats() // no-op when inner reader has no Reset hook
}

func TestVerifyingReader_InnerExposed(t *testing.T) {
	inner := &fixedReader{}
	v := cas.NewVerifyingReader(inner)
	if v.Inner() != inner {
		t.Fatal("Inner did not return wrapped reader")
	}
}

func TestNewVerifyingReader_NilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on nil reader")
		}
	}()
	_ = cas.NewVerifyingReader(nil)
}

func TestVerifyingReader_RewrapIsIdempotent(t *testing.T) {
	// The wrapper does not strip its own type when wrapped twice. Confirm it
	// still functions correctly when fed its own output (defense in depth).
	data := []byte("once")
	c := cidForRaw(t, data)
	inner := &fixedReader{get: map[string][]byte{c.String(): data}}
	v := cas.NewVerifyingReader(inner)
	doubled := cas.NewVerifyingReader(v)

	got, err := doubled.Get(context.Background(), c)
	if err != nil {
		t.Fatalf("doubled Get error: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("doubled Get = %q, want %q", got, data)
	}
}

// writingReader is the read+write surface tests need to exercise the
// VerifyingReader's write-forwarding path.
type writingReader struct {
	*fixedReader
	put       []byte
	putCodec  uint64
	putErr    error
	batchHits int
}

func (w *writingReader) Put(_ context.Context, data []byte) (cid.Cid, error) {
	if w.putErr != nil {
		return cid.Undef, w.putErr
	}
	w.put = append([]byte(nil), data...)
	w.putCodec = cid.Raw
	return cidForRawData(data), nil
}

func (w *writingReader) PutWithCodec(_ context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if w.putErr != nil {
		return cid.Undef, w.putErr
	}
	w.put = append([]byte(nil), data...)
	w.putCodec = codec
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(codec, hash), nil
}

func (w *writingReader) PutBatch(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	w.batchHits++
	results := make([]cas.PutResult, len(blocks))
	for i, b := range blocks {
		codec := b.Codec
		if codec == 0 {
			codec = cid.Raw
		}
		hash, err := mh.Sum(b.Data, mh.SHA2_256, -1)
		if err != nil {
			return nil, err
		}
		results[i] = cas.PutResult{CID: cid.NewCidV1(codec, hash), Status: cas.PutStatusStored}
	}
	return results, nil
}

func cidForRawData(data []byte) cid.Cid {
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

func TestVerifyingReader_PutForwardsWhenInnerImplementsWriter(t *testing.T) {
	inner := &writingReader{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	got, err := v.Put(context.Background(), []byte("payload"))
	if err != nil {
		t.Fatalf("Put returned error: %v", err)
	}
	if !got.Equals(cidForRawData([]byte("payload"))) {
		t.Fatalf("Put returned unexpected CID %s", got)
	}
	if string(inner.put) != "payload" {
		t.Fatalf("inner did not record Put, got %q", inner.put)
	}
}

func TestVerifyingReader_PutWithCodec_ForwardsTypedWriter(t *testing.T) {
	inner := &writingReader{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	const customCodec uint64 = 0x300005
	if _, err := v.PutWithCodec(context.Background(), []byte("p"), customCodec); err != nil {
		t.Fatalf("PutWithCodec error: %v", err)
	}
	if inner.putCodec != customCodec {
		t.Fatalf("inner codec = %x, want %x", inner.putCodec, customCodec)
	}
}

func TestVerifyingReader_PutWithCodec_RawFallsBackToPut(t *testing.T) {
	inner := &writingReader{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	if _, err := v.PutWithCodec(context.Background(), []byte("p"), cid.Raw); err != nil {
		t.Fatalf("PutWithCodec(Raw) error: %v", err)
	}
}

func TestVerifyingReader_PutOnPureReader_Errors(t *testing.T) {
	inner := &fixedReader{}
	v := cas.NewVerifyingReader(inner)
	if _, err := v.Put(context.Background(), []byte("nope")); err == nil {
		t.Fatal("expected Put error against pure-reader inner")
	}
	if _, err := v.PutWithCodec(context.Background(), []byte("nope"), 0x71); err == nil {
		t.Fatal("expected PutWithCodec error against pure-reader inner")
	}
	if _, err := v.PutBatch(context.Background(), []cas.Block{{Data: []byte{1}}}); err == nil {
		t.Fatal("expected PutBatch error against pure-reader inner")
	}
}

func TestVerifyingReader_PutBatchForwardsWhenInnerSupports(t *testing.T) {
	inner := &writingReader{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{{Data: []byte("a"), Codec: cid.Raw}}
	results, err := v.PutBatch(context.Background(), blocks)
	if err != nil {
		t.Fatalf("PutBatch returned error: %v", err)
	}
	if inner.batchHits != 1 {
		t.Fatalf("inner.batchHits = %d, want 1 (BatchWriter path)", inner.batchHits)
	}
	if len(results) != 1 {
		t.Fatalf("results = %v, want 1", results)
	}
}

// nonBatchWriter exposes Put but not PutBatch, exercising the fallback
// path that calls cas.PutBlocks via the wrapper.
type nonBatchWriter struct {
	*fixedReader
	puts [][]byte
}

func (n *nonBatchWriter) Put(_ context.Context, data []byte) (cid.Cid, error) {
	n.puts = append(n.puts, append([]byte(nil), data...))
	return cidForRawData(data), nil
}

func TestVerifyingReader_PutBatchFallsBackToPerBlockPut(t *testing.T) {
	inner := &nonBatchWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{
		{Data: []byte("alpha"), Codec: cid.Raw},
		{Data: []byte("beta"), Codec: cid.Raw},
	}
	results, err := v.PutBatch(context.Background(), blocks)
	if err != nil {
		t.Fatalf("PutBatch error: %v", err)
	}
	if len(results) != 2 || len(inner.puts) != 2 {
		t.Fatalf("expected 2 per-block writes, got results=%d puts=%d", len(results), len(inner.puts))
	}
}

// lyingWriter is a writer that returns CIDs unrelated to the bytes it was
// asked to store. The verifying wrapper must reject those CIDs so a
// downstream root commitment cannot point at content the writer never
// produced.
type lyingWriter struct {
	*fixedReader
	cidOverride cid.Cid
	codec       uint64
	codecHit    uint64
	calls       int
}

func (w *lyingWriter) Put(_ context.Context, _ []byte) (cid.Cid, error) {
	w.calls++
	w.codecHit = cid.Raw
	return w.cidOverride, nil
}

func (w *lyingWriter) PutWithCodec(_ context.Context, _ []byte, codec uint64) (cid.Cid, error) {
	w.calls++
	w.codecHit = codec
	return w.cidOverride, nil
}

func TestVerifyingReader_Put_RejectsLyingWriter(t *testing.T) {
	// Writer returns a CID for completely different bytes — what a
	// compromised remote CAS could do during upload.
	bogusCID := cidForRawData([]byte("not the bytes we wrote"))
	inner := &lyingWriter{fixedReader: &fixedReader{}, cidOverride: bogusCID}
	v := cas.NewVerifyingReader(inner)

	_, err := v.Put(context.Background(), []byte("real content"))
	if err == nil {
		t.Fatal("expected ErrCorruptedBlock for lying writer")
	}
	if !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("error %v is not ErrCorruptedBlock", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner Put calls = %d, want 1", inner.calls)
	}
}

func TestVerifyingReader_PutWithCodec_RejectsLyingWriter(t *testing.T) {
	const codec uint64 = 0x71 // dag-cbor
	// Build a CID with the correct codec but for unrelated bytes.
	hash, _ := mh.Sum([]byte("forged"), mh.SHA2_256, -1)
	bogusCID := cid.NewCidV1(codec, hash)
	inner := &lyingWriter{fixedReader: &fixedReader{}, cidOverride: bogusCID, codec: codec}
	v := cas.NewVerifyingReader(inner)

	_, err := v.PutWithCodec(context.Background(), []byte("genuine"), codec)
	if err == nil {
		t.Fatal("expected ErrCorruptedBlock for lying typed writer")
	}
	if !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("error %v is not ErrCorruptedBlock", err)
	}
}

func TestVerifyingReader_Put_RejectsUndefinedReturnedCID(t *testing.T) {
	inner := &lyingWriter{fixedReader: &fixedReader{}, cidOverride: cid.Undef}
	v := cas.NewVerifyingReader(inner)
	_, err := v.Put(context.Background(), []byte("anything"))
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock for undefined CID, got %v", err)
	}
}

// honestBatchWriter computes correct CIDs for every block it is asked to
// store; pairs with the lying counterpart to exercise PutBatch verification.
type honestBatchWriter struct {
	*fixedReader
}

func (h *honestBatchWriter) PutBatch(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(blocks))
	for i, b := range blocks {
		results[i] = cas.PutResult{CID: cidForRawData(b.Data), Status: cas.PutStatusStored}
	}
	return results, nil
}

func TestVerifyingReader_PutBatch_AcceptsHonestBatchWriter(t *testing.T) {
	inner := &honestBatchWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{
		{Data: []byte("a"), Codec: cid.Raw},
		{Data: []byte("b"), Codec: cid.Raw},
	}
	results, err := v.PutBatch(context.Background(), blocks)
	if err != nil {
		t.Fatalf("PutBatch error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
}

// lyingBatchWriter produces CIDs for one block that do not match the bytes,
// simulating a partially-compromised upload pipeline.
type lyingBatchWriter struct {
	*fixedReader
}

func (l *lyingBatchWriter) PutBatch(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(blocks))
	for i, b := range blocks {
		if i == 1 {
			results[i] = cas.PutResult{CID: cidForRawData([]byte("forged")), Status: cas.PutStatusStored}
			continue
		}
		results[i] = cas.PutResult{CID: cidForRawData(b.Data), Status: cas.PutStatusStored}
	}
	return results, nil
}

func TestVerifyingReader_PutBatch_RejectsLyingBatchWriter(t *testing.T) {
	inner := &lyingBatchWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{
		{Data: []byte("a"), Codec: cid.Raw},
		{Data: []byte("b"), Codec: cid.Raw},
	}
	_, err := v.PutBatch(context.Background(), blocks)
	if err == nil {
		t.Fatal("expected ErrCorruptedBlock for lying batch writer")
	}
	if !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("error %v is not ErrCorruptedBlock", err)
	}
}

// shortBatchWriter returns fewer results than blocks, mimicking a writer
// that drops entries silently.
type shortBatchWriter struct {
	*fixedReader
}

func (s *shortBatchWriter) PutBatch(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(blocks)-1)
	for i := range results {
		results[i] = cas.PutResult{CID: cidForRawData(blocks[i].Data), Status: cas.PutStatusStored}
	}
	return results, nil
}

func TestVerifyingReader_PutBatch_RejectsShortResultSlice(t *testing.T) {
	inner := &shortBatchWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{
		{Data: []byte("a"), Codec: cid.Raw},
		{Data: []byte("b"), Codec: cid.Raw},
	}
	_, err := v.PutBatch(context.Background(), blocks)
	if err == nil {
		t.Fatal("expected error for short result slice")
	}
	if !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("error %v is not ErrCorruptedBlock", err)
	}
}

// undefinedBatchWriter mimics a writer that returns an undefined CID for one
// of the blocks it was asked to store. Layout code copies PutResult.CID
// straight into chunk lists, so an undefined CID at this layer would mean a
// root referencing unresolvable content. The verifier must reject it.
type undefinedBatchWriter struct {
	*fixedReader
}

func (u *undefinedBatchWriter) PutBatch(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(blocks))
	for i, b := range blocks {
		if i == 1 {
			results[i] = cas.PutResult{} // undefined CID — must be rejected
			continue
		}
		results[i] = cas.PutResult{CID: cidForRawData(b.Data), Status: cas.PutStatusStored}
	}
	return results, nil
}

func TestVerifyingReader_PutBatch_RejectsUndefinedResults(t *testing.T) {
	inner := &undefinedBatchWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{
		{Data: []byte("a"), Codec: cid.Raw},
		{Data: []byte("b"), Codec: cid.Raw},
	}
	_, err := v.PutBatch(context.Background(), blocks)
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock, got %v", err)
	}
}

// rawCIDBatchWriter returns a cid.Raw CID for every block regardless of the
// codec the caller asked for. Hash-only verification would happily accept
// this because the bytes really do hash to that raw CID; the wrapper must
// instead reject because the codec does not match the request.
type rawCIDBatchWriter struct {
	*fixedReader
}

func (r *rawCIDBatchWriter) PutBatch(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(blocks))
	for i, b := range blocks {
		results[i] = cas.PutResult{CID: cidForRawData(b.Data), Status: cas.PutStatusStored}
	}
	return results, nil
}

func TestVerifyingReader_PutBatch_RejectsCodecMismatch(t *testing.T) {
	inner := &rawCIDBatchWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	blocks := []cas.Block{
		{Data: []byte("payload"), Codec: 0x71}, // dag-cbor request
	}
	_, err := v.PutBatch(context.Background(), blocks)
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock for codec mismatch, got %v", err)
	}
}

// codecSwappingTypedWriter accepts typed-block bytes but always returns a
// cid.Raw CID for them. This is the single-block analog of P2-a: hash-only
// verification would let it slip through, codec-aware verification must
// reject it.
type codecSwappingTypedWriter struct {
	*fixedReader
}

func (c *codecSwappingTypedWriter) Put(_ context.Context, data []byte) (cid.Cid, error) {
	return cidForRawData(data), nil
}

func (c *codecSwappingTypedWriter) PutWithCodec(_ context.Context, data []byte, _ uint64) (cid.Cid, error) {
	return cidForRawData(data), nil
}

func TestVerifyingReader_PutWithCodec_RejectsCodecMismatch(t *testing.T) {
	inner := &codecSwappingTypedWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	_, err := v.PutWithCodec(context.Background(), []byte("payload"), 0x71)
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock for codec mismatch, got %v", err)
	}
}

// weakHashTypedWriter returns a CID with the requested codec but a
// SHA-1 multihash. The bytes really do hash to that CID under SHA-1, so
// hash-prefix-based verification would accept it. The repo-wide write
// contract (CIDForBlock) pins SHA-256, so the wrapper must reject this.
type weakHashTypedWriter struct {
	*fixedReader
}

func (w *weakHashTypedWriter) Put(_ context.Context, data []byte) (cid.Cid, error) {
	hash, err := mh.Sum(data, mh.SHA1, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, hash), nil
}

func (w *weakHashTypedWriter) PutWithCodec(_ context.Context, data []byte, codec uint64) (cid.Cid, error) {
	hash, err := mh.Sum(data, mh.SHA1, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(codec, hash), nil
}

func TestVerifyingReader_Put_RejectsWeakMultihash(t *testing.T) {
	inner := &weakHashTypedWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	_, err := v.Put(context.Background(), []byte("payload"))
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock for SHA-1 downgrade, got %v", err)
	}
}

func TestVerifyingReader_PutWithCodec_RejectsWeakMultihash(t *testing.T) {
	inner := &weakHashTypedWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	_, err := v.PutWithCodec(context.Background(), []byte("payload"), 0x71)
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock for SHA-1 downgrade, got %v", err)
	}
}

// v0CIDWriter returns a CIDv0 (dag-pb + SHA-256 + base58btc) for raw
// uploads. CIDForBlock canonicalizes on CIDv1, so the wrapper must reject
// any CIDv0 even if hash and bytes line up — otherwise resolution code
// that branches on the version would see a different shape than the
// caller asked for.
type v0CIDWriter struct {
	*fixedReader
}

func (vw *v0CIDWriter) Put(_ context.Context, data []byte) (cid.Cid, error) {
	hash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV0(hash), nil
}

func TestVerifyingReader_Put_RejectsCIDv0Downgrade(t *testing.T) {
	inner := &v0CIDWriter{fixedReader: &fixedReader{}}
	v := cas.NewVerifyingReader(inner)
	_, err := v.Put(context.Background(), []byte("payload"))
	if err == nil || !errors.Is(err, cas.ErrCorruptedBlock) {
		t.Fatalf("expected ErrCorruptedBlock for CIDv0 downgrade, got %v", err)
	}
}
