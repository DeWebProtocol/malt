package writeplan

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type candidateFunc func(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error)

type blockFunc func(context.Context, []byte, uint64) (cid.Cid, error)

func (f blockFunc) PutWithCodec(ctx context.Context, body []byte, codec uint64) (cid.Cid, error) {
	return f(ctx, body, codec)
}

func (f candidateFunc) MaterializeAuthentication(ctx context.Context, c protocol.AuthenticationCandidate) (cid.Cid, error) {
	return f(ctx, c)
}

type batchFunc func(context.Context, protocol.AuthenticationBatch) (protocol.AuthenticationReceipt, error)

func (f batchFunc) MaterializeAuthenticationBatch(ctx context.Context, b protocol.AuthenticationBatch) (protocol.AuthenticationReceipt, error) {
	return f(ctx, b)
}

func fixture(t *testing.T) Plan {
	t.Helper()
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
	if err != nil {
		t.Fatal(err)
	}
	registry := engine.NewRegistry()
	if err := registry.Register(scheme); err != nil {
		t.Fatal(err)
	}
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.IPA256}, Entries: []engine.Entry{{Label: []byte("name"), Target: cid.MustParse("bafkqaaa")}}}
	candidate, err := authentication.Prepare(t.Context(), engine.New(registry), state)
	if err != nil {
		t.Fatal(err)
	}
	return Plan{Base: cid.MustParse(candidate.Root), Root: cid.MustParse(candidate.Root), Candidates: []protocol.AuthenticationCandidate{candidate}, TransactionID: "exact-retry"}
}

func TestPreparedBlocksPreflightBeforeAnyRemoteEffect(t *testing.T) {
	p := fixture(t)
	key, _ := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}).Sum([]byte("valid"))
	p.Blocks = []Block{Bytes(key, []byte("valid")), Bytes(key, []byte("corrupt"))}
	puts, candidates := 0, 0
	err := p.Persist(t.Context(), blockFunc(func(context.Context, []byte, uint64) (cid.Cid, error) { puts++; return key, nil }), candidateFunc(func(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error) {
		candidates++
		return p.Root, nil
	}))
	if err == nil || puts != 0 || candidates != 0 {
		t.Fatalf("invalid local plan had remote effects: %v, %d, %d", err, puts, candidates)
	}
}

func TestFailedAdapterCannotMutateRetryCandidate(t *testing.T) {
	p := fixture(t)
	failed := errors.New("lost acknowledgement")
	err := p.Persist(t.Context(), nil, candidateFunc(func(_ context.Context, c protocol.AuthenticationCandidate) (cid.Cid, error) {
		c.State.Entries[0].Label[0] = '!'
		return cid.Undef, failed
	}))
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	err = p.Persist(t.Context(), nil, candidateFunc(func(_ context.Context, c protocol.AuthenticationCandidate) (cid.Cid, error) {
		if string(c.State.Entries[0].Label) != "name" {
			t.Fatal("adapter corrupted the retry plan")
		}
		return p.Root, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestBatchReceiptBindsOriginalRequestEvenIfAdapterMutatesInput(t *testing.T) {
	p := fixture(t)
	_, err := p.PersistBatch(t.Context(), nil, batchFunc(func(_ context.Context, b protocol.AuthenticationBatch) (protocol.AuthenticationReceipt, error) {
		b.Candidates[0].State.Entries[0].Label[0] = '!'
		digest, err := b.Digest()
		if err != nil {
			return protocol.AuthenticationReceipt{}, err
		}
		return protocol.AuthenticationReceipt{Profile: protocol.AuthenticationReceiptProfile, Base: b.Base, Root: b.Root, TransactionID: b.TransactionID, Digest: digest, DurableBoundary: "test"}, nil
	}))
	if err == nil {
		t.Fatal("receipt acknowledged a different prepared request")
	}
}
