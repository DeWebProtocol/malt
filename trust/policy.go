package trust

import (
	"fmt"

	cid "github.com/ipfs/go-cid"
)

// Policy is the current accepted/candidate/observed root boundary. Promotion
// always requires an explicit local action; durable completion shares its fence.
type Policy interface {
	AcceptedRootFence
	ListStates() ([]RootState, error)
	GetState(string) (RootState, error)
	Trust(string, string, string, string, string) (RootState, error)
	AddCandidate(string, string, string, string) (RootState, error)
	AcceptCandidate(string, string, string) (RootState, error)
	ObserveHead(string, ObservedHead) (RootState, error)
	AcceptObserved(string, string, string, string, string) (RootState, error)
}

// AcceptedRootFence is the narrow local durability fence used when completing
// work that was verified against one accepted root. Implementations hold the
// same exclusion boundary as every accepted-root promotion for the callback.
type AcceptedRootFence interface {
	WithAcceptedRoot(alias, expectedRoot string, operation func() error) error
}

// AcceptedRoot resolves an alias to the currently accepted root CID. It never
// falls back to a candidate or a root supplied by an untrusted response.
func AcceptedRoot(policy Policy, alias string) (cid.Cid, RootState, error) {
	if policy == nil {
		return cid.Undef, RootState{}, fmt.Errorf("trusted-root policy is nil")
	}
	record, err := policy.GetState(alias)
	if err != nil {
		return cid.Undef, RootState{}, err
	}
	if record.Accepted == nil {
		return cid.Undef, record, ErrNoAcceptedRoot
	}
	root, err := cid.Parse(record.Accepted.Root)
	if err != nil {
		return cid.Undef, RootState{}, fmt.Errorf("accepted root for %q is invalid: %w", record.Alias, err)
	}
	return root, record, nil
}

var _ Policy = (*Store)(nil)
var _ AcceptedRootFence = (*Store)(nil)
