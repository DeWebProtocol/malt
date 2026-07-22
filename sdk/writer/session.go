package writer

import (
	"context"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt/mutation"
	cid "github.com/ipfs/go-cid"
)

// Session retains verified complete vectors and semantic materialization across
// mutations. Preparing a candidate never advances the session; only an exact
// durable receipt can advance it. Client trust promotion remains a separate
// higher-level policy.
type Session struct {
	mu      sync.Mutex
	runtime *Runtime
	loaded  bool
	current VerifiedUpdateView
}

func NewSession(runtime *Runtime) (*Session, error) {
	if runtime == nil {
		return nil, fmt.Errorf("client writer runtime is nil")
	}
	return &Session{runtime: runtime}, nil
}

// Load verifies and installs the accepted-root update view for this session.
func (s *Session) Load(ctx context.Context, view mutation.UpdateView) error {
	if s == nil {
		return fmt.Errorf("client writer session is nil")
	}
	verified, err := s.runtime.VerifyUpdateView(ctx, view)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = verified
	s.loaded = true
	return nil
}

// BaseRoot returns the currently retained accepted base. It is unchanged by
// Prepare and changes only after AcceptReceipt succeeds.
func (s *Session) BaseRoot() cid.Cid {
	if s == nil {
		return cid.Undef
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return cid.Undef
	}
	return s.current.View.BaseRoot
}

// Prepare computes a candidate against the session's current accepted base.
func (s *Session) Prepare(ctx context.Context, operationID string, intent mutation.SemanticIntent) (ComputeResult, error) {
	if s == nil {
		return ComputeResult{}, fmt.Errorf("client writer session is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return ComputeResult{}, fmt.Errorf("client writer session has no update view")
	}
	if !intent.BaseRoot.Equals(s.current.View.BaseRoot) {
		return ComputeResult{}, fmt.Errorf("intent base %s is stale; current base is %s", intent.BaseRoot, s.current.View.BaseRoot)
	}
	return s.runtime.ComputeBundle(ctx, operationID, s.current, intent)
}

// AcceptReceipt verifies a service's exact durable acknowledgement and only
// then advances retained writer state to the prepared next view.
func (s *Session) AcceptReceipt(receipt mutation.MaterializationReceipt, prepared ComputeResult) error {
	if s == nil {
		return fmt.Errorf("client writer session is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return fmt.Errorf("client writer session has no update view")
	}
	if !prepared.Bundle.View.BaseRoot.Equals(s.current.View.BaseRoot) {
		return fmt.Errorf("prepared bundle base is stale")
	}
	if err := receipt.Validate(prepared.Bundle); err != nil {
		return err
	}
	if !prepared.NextView.BaseRoot.Equals(prepared.Bundle.Candidate) {
		return fmt.Errorf("prepared next view does not match candidate")
	}
	s.current = VerifiedUpdateView{View: prepared.NextView}
	return nil
}

// Audit recomputes the retained complete vectors. Evaluators use this after a
// long-lived measured session; a failure invalidates the whole run.
func (s *Session) Audit(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("client writer session is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return fmt.Errorf("client writer session has no update view")
	}
	verified, err := s.runtime.VerifyUpdateView(ctx, s.current.View)
	if err != nil {
		return err
	}
	s.current = verified
	return nil
}
