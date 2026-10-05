package nodetest

import (
	"context"
	"reflect"
	"testing"

	"github.com/dewebprotocol/malt-client/nodeapi"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	sdk "github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

// DatasetFixture provides an isolated, initially empty writable branch and the
// candidate writer for the same execution scope.
type DatasetFixture struct {
	Branch nodeapi.DatasetBranch
	Writer nodeapi.AuthenticationWriter
	CAS    nodeapi.CAS
}

type DatasetFactory func(*testing.T) DatasetFixture

// RunDataset verifies publication, idempotency, stale-writer preservation and
// request binding through exactly the same API used by synchronization.
func RunDataset(t *testing.T, factory DatasetFactory) {
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
	materialize := func(t *testing.T, fixture DatasetFixture, content string) cid.Cid {
		t.Helper()
		target, err := fixture.CAS.Put(t.Context(), []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := sdk.Prepare(t.Context(), e, engine.State{
			Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.IPA256},
			Entries:    []engine.Entry{{Label: []byte("file"), Target: target}},
		})
		if err != nil {
			t.Fatal(err)
		}
		root, err := fixture.Writer.MaterializeAuthentication(t.Context(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		return root
	}

	t.Run("publication-and-idempotency", func(t *testing.T) {
		f := factory(t)
		binding := f.Branch.DatasetBinding()
		root := materialize(t, f, "first")
		head, err := f.Branch.ObserveHead(t.Context())
		if err != nil || head == nil {
			t.Fatalf("ObserveHead = %#v, %v", head, err)
		}
		if head.Root != "" || head.Revision != 0 {
			t.Fatal("materialization implicitly published a head")
		}
		request := nodeapi.ApplyRequest{OperationID: "publish-first", CandidateRoot: root.String()}
		result, err := f.Branch.ApplyCandidate(t.Context(), request)
		if err != nil || result == nil {
			t.Fatalf("ApplyCandidate = %#v, %v", result, err)
		}
		request.Branch = binding.Branch
		if err := nodeapi.ValidateApplyResult(binding.DatasetID, request, *result); err != nil {
			t.Fatal(err)
		}
		if result.Status != "fast_forward" || result.Head.Root != root.String() {
			t.Fatalf("first publication = %#v", result)
		}
		retry, err := f.Branch.ApplyCandidate(t.Context(), request)
		if err != nil || !reflect.DeepEqual(retry, result) {
			t.Fatalf("retry changed result: %#v, %v", retry, err)
		}
		head, err = f.Branch.ObserveHead(t.Context())
		if err != nil || !reflect.DeepEqual(head, &result.Head) {
			t.Fatalf("head = %#v, %v", head, err)
		}
		changed := request
		changed.Message = "a different operation"
		if _, err := f.Branch.ApplyCandidate(t.Context(), changed); err == nil {
			t.Fatal("operation ID accepted different metadata")
		}
	})

	t.Run("preserve-stale-candidate", func(t *testing.T) {
		f := factory(t)
		root := materialize(t, f, "initial")
		initial, err := f.Branch.ApplyCandidate(t.Context(), nodeapi.ApplyRequest{OperationID: "initial", CandidateRoot: root.String()})
		if err != nil {
			t.Fatal(err)
		}
		advance := func(content, operation string) *nodeapi.ApplyResult {
			root := materialize(t, f, content)
			result, err := f.Branch.ApplyCandidate(t.Context(), nodeapi.ApplyRequest{
				OperationID: operation, CandidateRoot: root.String(), BaseCommit: initial.Head.CommitID,
				BaseRoot: initial.Head.Root, BaseRevision: initial.Head.Revision, MergePolicy: "preserve",
			})
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
		current := advance("current", "advance")
		stale := advance("stale", "preserve")
		if current.Status != "fast_forward" || stale.Status != "branched" || stale.Branch == nil {
			t.Fatalf("current=%#v, stale=%#v", current, stale)
		}
		if !reflect.DeepEqual(stale.Head, current.Head) || stale.Branch.Root != stale.Candidate.Root {
			t.Fatal("conflict failed to preserve candidate and current head separately")
		}
	})

	t.Run("invalid-and-canceled-publications", func(t *testing.T) {
		f := factory(t)
		root := materialize(t, f, "unpublished")
		request := nodeapi.ApplyRequest{OperationID: "unpublished", CandidateRoot: root.String(), Branch: "other-branch"}
		if _, err := f.Branch.ApplyCandidate(t.Context(), request); err == nil {
			t.Fatal("accepted a write to another selected branch")
		}
		request.Branch = ""
		request.BaseRoot = root.String()
		if _, err := f.Branch.ApplyCandidate(t.Context(), request); err == nil {
			t.Fatal("accepted incomplete base tuple")
		}
		request.BaseRoot = ""
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := f.Branch.ApplyCandidate(ctx, request); err == nil {
			t.Fatal("accepted canceled publication")
		}
		head, err := f.Branch.ObserveHead(t.Context())
		if err != nil || head == nil || head.Root != "" || head.Revision != 0 {
			t.Fatalf("rejected write changed head: %#v, %v", head, err)
		}
	})
}
