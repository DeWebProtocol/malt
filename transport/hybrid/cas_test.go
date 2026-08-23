package hybrid

import (
	"bytes"
	"context"
	"errors"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

type recordingCAS struct {
	transportcap.CAS
	gets int
	has  int
	puts int
}

func (c *recordingCAS) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	c.gets++
	return c.CAS.Get(ctx, key)
}

func (c *recordingCAS) Has(ctx context.Context, key cid.Cid) (bool, error) {
	c.has++
	return c.CAS.Has(ctx, key)
}

func (c *recordingCAS) Put(ctx context.Context, body []byte) (cid.Cid, error) {
	c.puts++
	return c.CAS.Put(ctx, body)
}

func (c *recordingCAS) PutWithCodec(ctx context.Context, body []byte, codec uint64) (cid.Cid, error) {
	c.puts++
	return c.CAS.PutWithCodec(ctx, body, codec)
}

func TestCASUsesVerifiedCacheButPrimaryAuthority(t *testing.T) {
	primaryStore := casmemory.New()
	cacheStore := casmemory.New()
	body := []byte("hybrid payload")
	key, err := primaryStore.Put(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cacheStore.Put(t.Context(), body); err != nil {
		t.Fatal(err)
	}
	primary := &recordingCAS{CAS: primaryStore}
	cache := &recordingCAS{CAS: cacheStore}
	store, err := NewCAS(CASOptions{Primary: primary, Cache: cache})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) || primary.gets != 0 || cache.gets != 1 {
		t.Fatalf("Get = %q, %v; primary=%d cache=%d", got, err, primary.gets, cache.gets)
	}
	present, err := store.Has(t.Context(), key)
	if err != nil || !present || primary.has != 1 || cache.has != 0 {
		t.Fatalf("Has = %v, %v; primary=%d cache=%d", present, err, primary.has, cache.has)
	}
}

func TestCASFallsBackFromCorruptCacheAndRepairsIt(t *testing.T) {
	body := []byte("verified primary payload")
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	primary := casmemory.New()
	if _, err := primary.Put(t.Context(), body); err != nil {
		t.Fatal(err)
	}
	cache := &substitutingCAS{CAS: casmemory.New(), getBody: []byte("hostile cache")}
	var observed []error
	store, err := NewCAS(CASOptions{Primary: primary, Cache: cache, OnCacheError: func(err error) {
		observed = append(observed, err)
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), key)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if len(observed) != 1 || !errors.Is(observed[0], transportcap.ErrCorruptedBlock) {
		t.Fatalf("cache observations = %v", observed)
	}
	cache.getBody = nil
	cached, err := cache.Get(t.Context(), key)
	if err != nil || !bytes.Equal(cached, body) {
		t.Fatalf("repaired cache Get = %q, %v", cached, err)
	}
}

func TestCASRejectsSubstitutedPrimaryAndIgnoresCacheWriteFailure(t *testing.T) {
	wantBody := []byte("expected primary")
	want, err := clientcas.CIDForBlock(transportcap.Block{Data: wantBody})
	if err != nil {
		t.Fatal(err)
	}
	primary := &substitutingCAS{CAS: casmemory.New(), getBody: []byte("substituted primary")}
	store, err := NewCAS(CASOptions{Primary: primary, Cache: casmemory.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), want); !errors.Is(err, transportcap.ErrCorruptedBlock) {
		t.Fatalf("substituted primary error = %v, want ErrCorruptedBlock", err)
	}

	primary = &substitutingCAS{CAS: casmemory.New()}
	cacheFailure := errors.New("cache disk unavailable")
	cache := &failingPutCAS{CAS: casmemory.New(), err: cacheFailure}
	var observed error
	store, err = NewCAS(CASOptions{Primary: primary, Cache: cache, OnCacheError: func(err error) { observed = err }})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Put(t.Context(), wantBody)
	if err != nil || !got.Equals(want) {
		t.Fatalf("Put with cache failure = %s, %v", got, err)
	}
	if !errors.Is(observed, cacheFailure) {
		t.Fatalf("cache write observation = %v", observed)
	}
	present, err := primary.Has(t.Context(), want)
	if err != nil || !present {
		t.Fatalf("primary persistence = %v, %v", present, err)
	}
}

func TestCASRejectsTypedNilCapabilities(t *testing.T) {
	var typedNil *recordingCAS
	if _, err := NewCAS(CASOptions{Primary: typedNil, Cache: casmemory.New()}); err == nil {
		t.Fatal("typed-nil primary succeeded")
	}
	if _, err := NewCAS(CASOptions{Primary: casmemory.New(), Cache: typedNil}); err == nil {
		t.Fatal("typed-nil cache succeeded")
	}
}

func TestCASClassifiesMalformedPrimaryBatchResultsAsCorruption(t *testing.T) {
	body := []byte("batch-body")
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: body})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		primary *malformedBatchCAS
		call    func(*CAS) error
	}{
		{
			name: "put result length", primary: &malformedBatchCAS{CAS: casmemory.New(), putResults: []transportcap.PutResult{}},
			call: func(store *CAS) error {
				_, err := store.PutBatch(t.Context(), []transportcap.Block{{Data: body}})
				return err
			},
		},
		{
			name: "put status", primary: &malformedBatchCAS{CAS: casmemory.New(), putResults: []transportcap.PutResult{{CID: key, Status: "invented"}}},
			call: func(store *CAS) error {
				_, err := store.PutBatch(t.Context(), []transportcap.Block{{Data: body}})
				return err
			},
		},
		{
			name: "has result length", primary: &malformedBatchCAS{CAS: casmemory.New(), hasResults: []bool{}},
			call: func(store *CAS) error { _, err := store.HasBatch(t.Context(), []cid.Cid{key}); return err },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewCAS(CASOptions{Primary: test.primary, Cache: casmemory.New()})
			if err != nil {
				t.Fatal(err)
			}
			if err := test.call(store); !errors.Is(err, transportcap.ErrCorruptedBlock) {
				t.Fatalf("error = %v, want ErrCorruptedBlock", err)
			}
		})
	}
}

type substitutingCAS struct {
	transportcap.CAS
	getBody []byte
}

func (c *substitutingCAS) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if c.getBody != nil {
		return append([]byte(nil), c.getBody...), nil
	}
	return c.CAS.Get(ctx, key)
}

type failingPutCAS struct {
	transportcap.CAS
	err error
}

type malformedBatchCAS struct {
	transportcap.CAS
	putResults []transportcap.PutResult
	hasResults []bool
}

func (c *malformedBatchCAS) PutBatch(context.Context, []transportcap.Block) ([]transportcap.PutResult, error) {
	return append([]transportcap.PutResult(nil), c.putResults...), nil
}

func (c *malformedBatchCAS) HasBatch(context.Context, []cid.Cid) ([]bool, error) {
	return append([]bool(nil), c.hasResults...), nil
}

func (c *failingPutCAS) Put(context.Context, []byte) (cid.Cid, error) {
	return cid.Undef, c.err
}

func (c *failingPutCAS) PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error) {
	return cid.Undef, c.err
}
