// Package application composes trusted-root policy with UnixFS and Merkle-DAG
// capabilities into reusable native-runtime use cases. CLI and daemon adapters
// should remain thin presentation/control-plane layers over these services.
package application

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt-client/trust"
	cid "github.com/ipfs/go-cid"
)

// RootSelection is a caller-selected CID or a locally accepted alias. Alias is
// empty for explicit CIDs. Candidate roots are never considered during
// selection.
type RootSelection struct {
	Root  cid.Cid
	Alias string
}

// Roots owns reusable accepted/candidate/observed-root policy use cases.
type Roots struct {
	policy trust.Policy
}

func NewRoots(policy trust.Policy) (*Roots, error) {
	if policy == nil {
		return nil, fmt.Errorf("trusted-root policy is nil")
	}
	return &Roots{policy: policy}, nil
}

// NewExplicitRootSelector returns a selector that accepts caller-supplied CIDs
// without consulting local trusted-root state. It deliberately cannot resolve
// aliases or mutate accepted/candidate/observed-root records.
//
// This selector keeps explicit-CID operations available even when the optional
// alias store is missing, corrupt, or not writable.
func NewExplicitRootSelector() *Roots {
	return &Roots{}
}

// Select resolves an explicit CID or an accepted alias. It never falls back to
// a candidate root or an untrusted network value.
func (r *Roots) Select(raw string) (RootSelection, error) {
	if r == nil {
		return RootSelection{}, fmt.Errorf("trusted-root application is nil")
	}
	raw = strings.TrimSpace(raw)
	if root, err := cid.Parse(raw); err == nil {
		return RootSelection{Root: root}, nil
	}
	if r.policy == nil {
		return RootSelection{}, fmt.Errorf("%q is not an explicit CID", raw)
	}
	selected, err := r.LookupAlias(raw)
	if err != nil {
		return RootSelection{}, fmt.Errorf("%q is neither a CID nor a trusted-root alias: %w", raw, err)
	}
	return selected, nil
}

// LookupAlias resolves only a locally accepted alias. Unlike Select, it never
// interprets a CID-shaped string as an explicit root. Callers must use this
// entry point when the input is explicitly typed as an alias, such as --alias.
func (r *Roots) LookupAlias(alias string) (RootSelection, error) {
	if r == nil || r.policy == nil {
		return RootSelection{}, fmt.Errorf("trusted-root application is nil")
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return RootSelection{}, fmt.Errorf("trusted-root alias is empty")
	}
	root, record, err := trust.AcceptedRoot(r.policy, alias)
	if err != nil {
		return RootSelection{}, fmt.Errorf("lookup trusted-root alias %q: %w", alias, err)
	}
	return RootSelection{Root: root, Alias: record.Alias}, nil
}

// AcceptedRoot resolves an alias to its locally accepted root. It exists as a
// narrow capability for application services, such as encrypted backup sync,
// that must never select a root observed from an untrusted transport.
func (r *Roots) AcceptedRoot(alias string) (cid.Cid, error) {
	selected, err := r.LookupAlias(alias)
	if err != nil {
		return cid.Undef, err
	}
	return selected.Root, nil
}

// CompleteIfAccepted runs a short local durable completion while excluding all
// accepted-root promotions through the same trust-store fence. False means the
// selected root changed before the callback, so the callback was not run.
func (r *Roots) CompleteIfAccepted(alias string, expected cid.Cid, operation func() error) (bool, error) {
	if r == nil || r.policy == nil {
		return false, fmt.Errorf("trusted-root application is nil")
	}
	if !expected.Defined() {
		return false, fmt.Errorf("expected accepted root is undefined")
	}
	fence, ok := r.policy.(trust.AcceptedRootFence)
	if !ok {
		return false, fmt.Errorf("trusted-root policy does not support accepted-root fencing")
	}
	err := fence.WithAcceptedRoot(alias, expected.String(), operation)
	if errors.Is(err, trust.ErrAcceptedRootChanged) {
		return false, nil
	}
	return err == nil, err
}

func (r *Roots) List() ([]trust.Record, error) {
	if r == nil || r.policy == nil {
		return nil, fmt.Errorf("trusted-root application is nil")
	}
	return r.policy.List()
}

// ListStates returns explicit accepted/candidate/observed trust-plane states.
func (r *Roots) ListStates() ([]trust.RootState, error) {
	if r == nil || r.policy == nil {
		return nil, fmt.Errorf("trusted-root application is nil")
	}
	policy, ok := r.policy.(trust.ObservationPolicy)
	if !ok {
		return nil, fmt.Errorf("trusted-root policy does not support observations")
	}
	return policy.ListStates()
}

func (r *Roots) Get(alias string) (trust.Record, error) {
	if r == nil || r.policy == nil {
		return trust.Record{}, fmt.Errorf("trusted-root application is nil")
	}
	return r.policy.Get(alias)
}

// GetState returns the explicit accepted/candidate/observed trust-plane state.
func (r *Roots) GetState(alias string) (trust.RootState, error) {
	if r == nil || r.policy == nil {
		return trust.RootState{}, fmt.Errorf("trusted-root application is nil")
	}
	policy, ok := r.policy.(trust.ObservationPolicy)
	if !ok {
		return trust.RootState{}, fmt.Errorf("trusted-root policy does not support observations")
	}
	return policy.GetState(alias)
}

func (r *Roots) Trust(alias, root, profile, gateway, source string) (trust.Record, error) {
	if r == nil || r.policy == nil {
		return trust.Record{}, fmt.Errorf("trusted-root application is nil")
	}
	return r.policy.Trust(alias, root, profile, gateway, source)
}

// RecordCandidate records an untrusted locally computed result without
// accepting it. A defined base must still be the alias's accepted root. An
// undefined base denotes a bootstrap candidate and is valid only while the
// alias has no accepted root.
func (r *Roots) RecordCandidate(alias string, candidateRoot, baseRoot cid.Cid, source string) (trust.Record, error) {
	if r == nil || r.policy == nil {
		return trust.Record{}, fmt.Errorf("trusted-root application is nil")
	}
	if strings.TrimSpace(alias) == "" {
		return trust.Record{}, fmt.Errorf("candidate alias is empty")
	}
	if !candidateRoot.Defined() {
		return trust.Record{}, fmt.Errorf("candidate root must be defined")
	}
	base := ""
	if baseRoot.Defined() {
		base = baseRoot.String()
	}
	return r.policy.AddCandidate(alias, candidateRoot.String(), base, source)
}

// ObserveCandidate records an untrusted root while exposing only an error
// result to services that do not otherwise need trust-store record details.
func (r *Roots) ObserveCandidate(alias string, candidateRoot, baseRoot cid.Cid, source string) error {
	_, err := r.RecordCandidate(alias, candidateRoot, baseRoot, source)
	return err
}

// HasCandidate confirms exact durable candidate provenance without creating or
// rebasing candidate state. An undefined base denotes a bootstrap candidate.
func (r *Roots) HasCandidate(alias string, candidateRoot, baseRoot cid.Cid) (bool, error) {
	if r == nil || r.policy == nil || !candidateRoot.Defined() {
		return false, fmt.Errorf("candidate inspection request is incomplete")
	}
	policy, ok := r.policy.(trust.ObservationPolicy)
	if !ok {
		return false, fmt.Errorf("trusted-root policy does not support candidate-state inspection")
	}
	state, err := policy.GetState(alias)
	if err != nil {
		return false, err
	}
	base := ""
	if baseRoot.Defined() {
		base = baseRoot.String()
	}
	for _, candidate := range state.Candidates {
		if candidate.Root == candidateRoot.String() && candidate.BaseRoot == base {
			return true, nil
		}
	}
	return false, nil
}

// ObserveHead records an untrusted remote dataset head without creating a
// candidate or changing the accepted root.
func (r *Roots) ObserveHead(alias, source, datasetID, branch, commitID string, root cid.Cid, revision uint64) error {
	if r == nil || r.policy == nil {
		return fmt.Errorf("trusted-root application is nil")
	}
	policy, ok := r.policy.(trust.ObservationPolicy)
	if !ok {
		return fmt.Errorf("trusted-root policy does not support observations")
	}
	rootText := ""
	if root.Defined() {
		rootText = root.String()
	}
	_, err := policy.ObserveHead(alias, trust.ObservedHead{
		Source: source, DatasetID: datasetID, Branch: branch,
		CommitID: commitID, Root: rootText, Revision: revision,
	})
	return err
}

// AcceptCandidate is the only application use case that promotes a recorded
// candidate. Callers must invoke it as an explicit local action.
func (r *Roots) AcceptCandidate(alias string, candidate cid.Cid, source string) (trust.Record, error) {
	if r == nil || r.policy == nil {
		return trust.Record{}, fmt.Errorf("trusted-root application is nil")
	}
	if !candidate.Defined() {
		return trust.Record{}, fmt.Errorf("candidate root is undefined")
	}
	return r.policy.AcceptCandidate(alias, candidate.String(), source)
}

// AcceptObserved explicitly promotes a recorded remote observation. It cannot
// accept a root that was never recorded by ObserveHead.
func (r *Roots) AcceptObserved(alias string, observed cid.Cid, profile, gateway, source string) (trust.Record, error) {
	if r == nil || r.policy == nil {
		return trust.Record{}, fmt.Errorf("trusted-root application is nil")
	}
	if !observed.Defined() {
		return trust.Record{}, fmt.Errorf("observed root is undefined")
	}
	policy, ok := r.policy.(trust.ObservationPolicy)
	if !ok {
		return trust.Record{}, fmt.Errorf("trusted-root policy does not support observations")
	}
	return policy.AcceptObserved(alias, observed.String(), profile, gateway, source)
}
