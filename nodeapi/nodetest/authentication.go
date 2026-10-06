package nodetest

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-client/nodeapi"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	sdk "github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

// AuthenticationService is the combination exercised by RunAuthentication.
// Applications should continue to consume only the capabilities they need.
type AuthenticationService interface {
	nodeapi.Authentication
	nodeapi.AuthenticationWriter
	nodeapi.AuthenticationBatch
}

type AuthenticationFactory func(*testing.T) AuthenticationService

// RunAuthentication exercises real Core candidates and locally verifies the
// same proof contract through an in-process service or an RPC adapter. The
// fixture must support IPA256 Prefix state and isolated batch transaction IDs.
func RunAuthentication(t *testing.T, factory AuthenticationFactory) {
	t.Helper()
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileDirect)
	if err != nil {
		t.Fatal(err)
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		t.Fatal(err)
	}
	e := engine.New(profiles)
	prepare := func(t *testing.T, label string) protocol.AuthenticationCandidate {
		t.Helper()
		state := engine.State{Descriptor: maltcid.RootDescriptor{
			Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.IPA256,
		}, Entries: []engine.Entry{{Label: []byte(label), Target: cid.MustParse("bafkqaaa")}}}
		candidate, err := sdk.Prepare(t.Context(), e, state)
		if err != nil {
			t.Fatal(err)
		}
		return candidate
	}

	t.Run("materialize-export-query", func(t *testing.T) {
		s := factory(t)
		c := prepare(t, "node/api")
		root, err := s.MaterializeAuthentication(t.Context(), c)
		if err != nil || root.String() != c.Root {
			t.Fatalf("materialization = %s, %v", root, err)
		}
		for i := 0; i < 2; i++ {
			got, err := s.AuthenticationCandidate(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if err := nodeapi.ValidateCandidateResult(root, got); err != nil {
				t.Fatal(err)
			}
			recomputed, err := sdk.Prepare(t.Context(), e, got.State)
			if err != nil || recomputed.Root != c.Root {
				t.Fatalf("exported state does not recompute to Root: %v", err)
			}
			// The result belongs to this caller; mutating it must not corrupt storage.
			got.State.Entries[0].Label[0] ^= 0xff
		}
		for _, label := range []string{"node/api", "missing"} {
			bytes := []byte(label)
			q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: c.Root, Operation: "binding", Label: &bytes}
			result, err := s.Authenticate(t.Context(), q)
			if err != nil || result == nil {
				t.Fatalf("query = %v, %v", result, err)
			}
			ok, err := sdk.Verify(e, q, *result)
			if err != nil || !ok {
				t.Fatalf("local verification = %v, %v", ok, err)
			}
		}
	})

	t.Run("batch-receipt-and-retry", func(t *testing.T) {
		s := factory(t)
		base, next := prepare(t, "base"), prepare(t, "next")
		if _, err := s.MaterializeAuthentication(t.Context(), base); err != nil {
			t.Fatal(err)
		}
		next.Previous = base.Root
		batch := protocol.AuthenticationBatch{Profile: protocol.AuthenticationBatchProfile, TransactionID: "node-api-batch", Base: base.Root, Root: next.Root, Candidates: []protocol.AuthenticationCandidate{next}}
		first, err := s.MaterializeAuthenticationBatch(t.Context(), batch)
		if err != nil {
			t.Fatal(err)
		}
		if err := first.Validate(batch); err != nil {
			t.Fatal(err)
		}
		retry, err := s.MaterializeAuthenticationBatch(t.Context(), batch)
		if err != nil || retry != first {
			t.Fatalf("idempotent receipt = %#v, %v; want %#v", retry, err, first)
		}
		other := prepare(t, "different")
		other.Previous = base.Root
		changed := batch
		changed.Root, changed.Candidates = other.Root, []protocol.AuthenticationCandidate{other}
		if _, err := s.MaterializeAuthenticationBatch(t.Context(), changed); err == nil {
			t.Fatal("reused transaction ID accepted a different batch")
		}
		if _, err := s.AuthenticationCandidate(t.Context(), cid.MustParse(other.Root)); err == nil {
			t.Fatal("rejected batch persisted a new candidate")
		}
	})

	t.Run("invalid-and-canceled-writes-have-no-effect", func(t *testing.T) {
		s := factory(t)
		c := prepare(t, "unwritten")
		bad := c
		bad.State.Descriptor.Layout = maltcid.Positional
		if _, err := s.MaterializeAuthentication(t.Context(), bad); err == nil {
			t.Fatal("accepted mismatched Root/state descriptor")
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := s.MaterializeAuthentication(ctx, c); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled materialization: %v", err)
		}
		if _, err := s.AuthenticationCandidate(t.Context(), cid.MustParse(c.Root)); err == nil {
			t.Fatal("invalid or canceled materialization stored a candidate")
		}
		if _, err := s.Authenticate(t.Context(), protocol.AuthenticationRequest{}); err == nil {
			t.Fatal("accepted invalid query")
		}
	})
}
