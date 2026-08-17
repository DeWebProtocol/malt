package trust

import (
	"fmt"

	cid "github.com/ipfs/go-cid"
)

// Policy is the compatibility accepted/candidate root boundary. Network
// transports and application readers may consult it, but they cannot promote
// a candidate without an explicit AcceptCandidate call. Observation-aware
// callers use the additive ObservationPolicy.
type Policy interface {
	List() ([]Record, error)
	Get(string) (Record, error)
	Trust(string, string, string, string, string) (Record, error)
	AddCandidate(string, string, string, string) (Record, error)
	AcceptCandidate(string, string, string) (Record, error)
}

// ObservationPolicy extends the compatibility Policy with explicit remote
// observation state. Recording an observation cannot mutate an accepted root;
// AcceptObserved is a separate explicit local action.
type ObservationPolicy interface {
	Policy
	ListStates() ([]RootState, error)
	GetState(string) (RootState, error)
	ObserveHead(string, ObservedHead) (Record, error)
	AcceptObserved(string, string, string, string, string) (Record, error)
}

// AcceptedRoot resolves an alias to the currently accepted root CID. It never
// falls back to a candidate or a root supplied by an untrusted response.
func AcceptedRoot(policy Policy, alias string) (cid.Cid, Record, error) {
	if policy == nil {
		return cid.Undef, Record{}, fmt.Errorf("trusted-root policy is nil")
	}
	record, err := policy.Get(alias)
	if err != nil {
		return cid.Undef, Record{}, err
	}
	if record.AcceptedRoot == "" {
		return cid.Undef, record, ErrNoAcceptedRoot
	}
	root, err := cid.Parse(record.AcceptedRoot)
	if err != nil {
		return cid.Undef, Record{}, fmt.Errorf("accepted root for %q is invalid: %w", record.Alias, err)
	}
	return root, record, nil
}

var _ Policy = (*Store)(nil)
var _ ObservationPolicy = (*Store)(nil)
