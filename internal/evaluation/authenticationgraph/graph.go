// Package authenticationgraph composes retained Core Writers for bounded,
// disposable-Gateway evaluations. It owns no protocol or accepted-root policy.
package authenticationgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const (
	MaxObjects = 4096
	MaxEntries = 65536
	MaxDepth   = 256
	MaxBytes   = 64 << 20
)

type Remote interface {
	AuthenticationCandidate(context.Context, cid.Cid) (gatewaytransport.CandidateResponse, error)
	SubmitAuthenticationBatch(context.Context, protocol.AuthenticationBatch) (gatewaytransport.BatchResponse, error)
}

// View contains original typed inputs, without copying complete node vectors.
// It is an independent snapshot; changing it cannot mutate retained Writers.
type View struct {
	Root   cid.Cid
	States map[string]engine.State
}

func (v View) State(root cid.Cid) (engine.State, error) {
	state, ok := v.States[root.String()]
	if !ok {
		return engine.State{}, fmt.Errorf("authentication graph omits Root %s", root)
	}
	return state, nil
}

type object struct {
	writer *authentication.Writer
	state  engine.State
	bytes  uint64
}
type graph struct {
	root    cid.Cid
	objects map[string]object
}

type LoadMetrics struct{ FetchNS, ImportNS, WireBytes, Objects uint64 }
type EditMetrics struct{ ApplyNS, ExportNS, Candidates, CandidateBytes uint64 }
type Result struct {
	Base, Root        cid.Cid
	Receipt           protocol.AuthenticationReceipt
	Idempotent        bool
	Computation       EditMetrics
	Submission        gatewaytransport.BatchResponse
	SubmitRoundTripNS uint64
}

// Session advances only after a receipt binds the exact submitted batch and
// evaluator durable boundary. This is materialization acknowledgement only.
type Session struct {
	mu      sync.Mutex
	remote  Remote
	engine  *engine.Engine
	current *graph
}

func New(remote Remote, e *engine.Engine) (*Session, error) {
	if remote == nil || e == nil {
		return nil, errors.New("authentication graph needs a remote and engine")
	}
	return &Session{remote: remote, engine: e}, nil
}
func (s *Session) Load(ctx context.Context, root cid.Cid) (LoadMetrics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, metrics, err := s.fetch(ctx, root)
	if err == nil {
		s.current = next
	}
	return metrics, err
}
func (s *Session) fetch(ctx context.Context, root cid.Cid) (*graph, LoadMetrics, error) {
	var metrics LoadMetrics
	if err := s.engine.CheckRoot(root); err != nil {
		return nil, metrics, err
	}
	next := &graph{root: root, objects: map[string]object{}}
	visiting := map[string]bool{}
	heights := map[string]int{}
	var entries uint64
	var visit func(cid.Cid, int) error
	visit = func(root cid.Cid, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > MaxDepth {
			return errors.New("authentication graph exceeds depth bound")
		}
		key := root.String()
		if visiting[key] {
			return errors.New("authentication graph contains a cycle")
		}
		if _, ok := next.objects[key]; ok {
			if depth+heights[key] > MaxDepth {
				return errors.New("shared subtree exceeds depth bound")
			}
			return nil
		}
		if len(next.objects)+len(visiting) >= MaxObjects {
			return errors.New("authentication graph exceeds object bound")
		}
		visiting[key] = true
		started := time.Now()
		response, err := s.remote.AuthenticationCandidate(ctx, root)
		metrics.FetchNS += elapsed(started)
		if err != nil {
			return err
		}
		candidate := response.Candidate
		if candidate.Root != key {
			return errors.New("candidate response substitutes the selected Root")
		}
		// Count normalized bytes as well as wire bytes; an in-process Remote is not
		// allowed to bypass the same memory bound as the HTTP adapter.
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return err
		}
		size := max(response.WireBytes, uint64(len(encoded)))
		if size > MaxBytes-metrics.WireBytes {
			return errors.New("authentication graph exceeds byte bound")
		}
		metrics.WireBytes += size
		if uint64(len(candidate.State.Entries)) > MaxEntries-entries {
			return errors.New("authentication graph exceeds entry bound")
		}
		entries += uint64(len(candidate.State.Entries))
		started = time.Now()
		writer, err := authentication.NewWriter(ctx, s.engine, candidate)
		metrics.ImportNS += elapsed(started)
		if err != nil {
			return fmt.Errorf("authenticate candidate %s: %w", key, err)
		}
		for _, entry := range candidate.State.Entries {
			if _, _, err := maltcid.ParseRoot(entry.Target); err == nil {
				if err := visit(entry.Target, depth+1); err != nil {
					return err
				}
				heights[key] = max(heights[key], 1+heights[entry.Target.String()])
			}
		}
		next.objects[key] = object{writer: writer, state: cloneState(candidate.State), bytes: size}
		delete(visiting, key)
		metrics.Objects++
		return nil
	}
	if err := visit(root, 0); err != nil {
		return nil, metrics, err
	}
	return next, metrics, nil
}
func (s *Session) Snapshot() (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return View{}, errors.New("authentication graph is not loaded")
	}
	return snapshot(s.current), nil
}
func snapshot(g *graph) View {
	v := View{Root: g.root, States: make(map[string]engine.State, len(g.objects))}
	for key, object := range g.objects {
		v.States[key] = cloneState(object.state)
	}
	return v
}

// Audit reimports the durable graph independently, without changing the session.
func (s *Session) Audit(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return errors.New("authentication graph is not loaded")
	}
	_, _, err := s.fetch(ctx, s.current.root)
	return err
}

// Edit is private local computation against one immutable graph. Callers apply
// children before explicitly replacing the selected parent edge; aliased roots
// therefore retain ordinary copy-on-write behavior.
type Edit struct {
	owner      *Session
	base       *graph
	pending    map[string]object
	candidates []protocol.AuthenticationCandidate
	metrics    EditMetrics
}

func (s *Session) Begin() (*Edit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil, errors.New("authentication graph is not loaded")
	}
	return &Edit{owner: s, base: s.current, pending: map[string]object{}}, nil
}
func (e *Edit) get(root cid.Cid) (object, error) {
	if o, ok := e.pending[root.String()]; ok {
		return o, nil
	}
	if o, ok := e.base.objects[root.String()]; ok {
		return o, nil
	}
	return object{}, fmt.Errorf("edit refers to an unavailable Root %s", root)
}
func (e *Edit) State(root cid.Cid) (engine.State, error) {
	o, err := e.get(root)
	return cloneState(o.state), err
}
func (e *Edit) Metrics() EditMetrics { return e.metrics }
func (e *Edit) Apply(ctx context.Context, root cid.Cid, delta authentication.Delta) (cid.Cid, error) {
	o, err := e.get(root)
	if err != nil {
		return cid.Undef, err
	}
	started := time.Now()
	writer, err := o.writer.Apply(ctx, delta)
	e.metrics.ApplyNS += elapsed(started)
	if err != nil {
		return cid.Undef, err
	}
	if writer.Root().Equals(root) {
		return root, nil
	}
	return e.retain(ctx, writer)
}
func (e *Edit) Build(ctx context.Context, state engine.State) (cid.Cid, error) {
	if len(state.Entries) > MaxEntries {
		return cid.Undef, errors.New("new state exceeds entry bound")
	}
	started := time.Now()
	writer, err := authentication.BuildWriter(ctx, e.owner.engine, state)
	e.metrics.ApplyNS += elapsed(started)
	if err != nil {
		return cid.Undef, err
	}
	return e.retain(ctx, writer)
}
func (e *Edit) retain(ctx context.Context, writer *authentication.Writer) (cid.Cid, error) {
	root := writer.Root()
	if _, ok := e.pending[root.String()]; ok {
		return root, nil
	}
	if _, ok := e.base.objects[root.String()]; ok {
		return root, nil
	}
	if len(e.pending) >= MaxObjects {
		return cid.Undef, errors.New("edit exceeds candidate bound")
	}
	started := time.Now()
	candidate, err := writer.Export(ctx)
	e.metrics.ExportNS += elapsed(started)
	if err != nil {
		return cid.Undef, err
	}
	data, err := json.Marshal(candidate)
	if err != nil {
		return cid.Undef, err
	}
	size := uint64(len(data))
	if size > MaxBytes-e.metrics.CandidateBytes {
		return cid.Undef, errors.New("edit exceeds candidate byte bound")
	}
	e.metrics.CandidateBytes += size
	e.metrics.Candidates++
	e.pending[root.String()] = object{writer: writer, state: candidate.State, bytes: size}
	e.candidates = append(e.candidates, candidate)
	return root, nil
}
func (e *Edit) graph(ctx context.Context, root cid.Cid) (*graph, error) {
	next := &graph{root: root, objects: map[string]object{}}
	visiting := map[string]bool{}
	heights := map[string]int{}
	var entries, size uint64
	var visit func(cid.Cid, int) error
	visit = func(root cid.Cid, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > MaxDepth {
			return errors.New("new graph exceeds depth bound")
		}
		key := root.String()
		if visiting[key] {
			return errors.New("new graph contains a cycle")
		}
		if _, ok := next.objects[key]; ok {
			if depth+heights[key] > MaxDepth {
				return errors.New("shared subtree exceeds depth bound")
			}
			return nil
		}
		if len(next.objects)+len(visiting) >= MaxObjects {
			return errors.New("new graph exceeds object bound")
		}
		o, err := e.get(root)
		if err != nil {
			return err
		}
		if uint64(len(o.state.Entries)) > MaxEntries-entries || o.bytes > MaxBytes-size {
			return errors.New("new graph exceeds entry or byte bound")
		}
		entries += uint64(len(o.state.Entries))
		size += o.bytes
		visiting[key] = true
		for _, entry := range o.state.Entries {
			if _, _, err := maltcid.ParseRoot(entry.Target); err == nil {
				if err := visit(entry.Target, depth+1); err != nil {
					return err
				}
				heights[key] = max(heights[key], 1+heights[entry.Target.String()])
			}
		}
		next.objects[key] = o
		delete(visiting, key)
		return nil
	}
	if err := visit(root, 0); err != nil {
		return nil, err
	}
	return next, nil
}

// Preview runs closure and bounds checks before submission. It is local state,
// not evidence of persistence, publication, or trusted-root acceptance.
func (e *Edit) Preview(ctx context.Context, root cid.Cid) (View, error) {
	g, err := e.graph(ctx, root)
	if err != nil {
		return View{}, err
	}
	return snapshot(g), nil
}
func (s *Session) Submit(ctx context.Context, transaction string, edit *Edit, root cid.Cid) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if edit == nil || edit.owner != s || edit.base != s.current {
		return Result{}, errors.New("edit does not match current session state")
	}
	next, err := edit.graph(ctx, root)
	if err != nil {
		return Result{}, err
	}
	batch := protocol.AuthenticationBatch{Profile: protocol.AuthenticationBatchProfile, TransactionID: transaction, Base: s.current.root.String(), Root: root.String(), Candidates: edit.candidates}
	if err := batch.Validate(); err != nil {
		return Result{}, err
	}
	started := time.Now()
	response, err := s.remote.SubmitAuthenticationBatch(ctx, batch)
	roundTrip := elapsed(started)
	if err != nil {
		return Result{}, err
	}
	if err := response.Receipt.Validate(batch); err != nil {
		return Result{}, err
	}
	if response.Receipt.DurableBoundary != gatewaytransport.AuthenticationDurableBoundary {
		return Result{}, errors.New("receipt has an unexpected durable boundary")
	}
	result := Result{Base: s.current.root, Root: root, Receipt: response.Receipt, Idempotent: response.Idempotent, Computation: edit.metrics, Submission: response, SubmitRoundTripNS: roundTrip}
	s.current = next
	return result, nil
}
func cloneState(state engine.State) engine.State {
	state.Entries = append([]engine.Entry{}, state.Entries...)
	for i := range state.Entries {
		state.Entries[i].Input.Data = bytes.Clone(state.Entries[i].Input.Data)
	}
	return state
}
func elapsed(started time.Time) uint64 { return uint64(max(time.Since(started), 0)) }
