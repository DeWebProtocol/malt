package nodeapi

import (
	"fmt"

	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

// CandidateRoot validates the complete canonical Core candidate before a node
// or RPC adapter starts materialization. Validation does not verify a transition
// or authorize publication of the resulting Root.
func CandidateRoot(candidate protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := candidate.Validate(); err != nil {
		return cid.Undef, err
	}
	return cid.Decode(candidate.Root)
}

// ValidateCandidateResult binds a candidate response to the selected Root.
// Consumers that use its state as a verified writer base must additionally run
// Core verification; schema and identity validation alone are not evidence.
func ValidateCandidateResult(root cid.Cid, candidate *protocol.AuthenticationCandidate) error {
	if _, _, err := maltcid.ParseRoot(root); err != nil {
		return err
	}
	if candidate == nil {
		return fmt.Errorf("node returned a nil authentication candidate")
	}
	returned, err := CandidateRoot(*candidate)
	if err != nil {
		return err
	}
	if !returned.Equals(root) {
		return fmt.Errorf("candidate view does not match selected Root")
	}
	return nil
}

// ValidateMaterializedRoot binds a node's write result to the exact candidate.
// It does not turn an operational acknowledgement into a state-transition proof.
func ValidateMaterializedRoot(expected, returned cid.Cid) error {
	if !expected.Defined() || !returned.Equals(expected) {
		return fmt.Errorf("materialization receipt changed candidate Root")
	}
	return nil
}
