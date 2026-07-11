package malt

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	structure "github.com/dewebprotocol/malt/auth/semantic"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/graph/writer"
	cid "github.com/ipfs/go-cid"
)

// MapReader is the minimum execution-plane capability needed to authenticate
// one keyed relation. Stateful map implementations satisfy it without exposing
// their commit or mutation methods through Engine.
type MapReader interface {
	Prove(context.Context, string, cid.Cid, arcset.Path) (mapping.Binding, structure.Proof, error)
}

// ListReader is the minimum execution-plane capability needed to authenticate
// one stable list position.
type ListReader interface {
	Prove(context.Context, string, cid.Cid, uint64) (list.Query, structure.Proof, error)
}

// MeasuredListReader extends ListReader with authenticated byte ranges.
type MeasuredListReader interface {
	ListReader
	ProveRange(context.Context, string, cid.Cid, uint64, *uint64) (list.RangeResult, structure.Proof, error)
}

// MutationApplier is the minimum execution-plane write capability consumed by
// Engine. The engine supplies placement scope separately from the mutation.
type MutationApplier interface {
	Apply(context.Context, string, writer.SemanticMutation) (writer.WriteReceipt, error)
}

// ProofVerifier is the portable verifier surface consumed by the core facade.
// Implementations must not require ArcTable, CAS, server, or daemon state.
type ProofVerifier interface {
	VerifyProofList(context.Context, prooflist.ProofList) (bool, error)
}

// EngineOptions supplies execution-plane implementations while keeping their
// storage scope outside canonical queries and mutations.
type EngineOptions struct {
	Scope    string
	Maps     MapReader
	Lists    ListReader
	Writer   MutationApplier
	Verifier ProofVerifier
}

// Engine composes typed MALT read, mutation, and verification operations. The
// engine is untrusted execution state; callers accept results only after
// VerifyRead succeeds against their trusted root.
type Engine struct {
	scope    string
	maps     MapReader
	lists    ListReader
	writer   MutationApplier
	verifier ProofVerifier
}

// NewEngine creates a reusable MALT execution facade.
func NewEngine(opts EngineOptions) (*Engine, error) {
	if opts.Scope == "" {
		return nil, fmt.Errorf("MALT engine scope is empty")
	}
	if opts.Maps == nil && opts.Lists == nil && opts.Writer == nil && opts.Verifier == nil {
		return nil, fmt.Errorf("MALT engine has no configured capabilities")
	}
	return &Engine{
		scope:    opts.Scope,
		maps:     opts.Maps,
		lists:    opts.Lists,
		writer:   opts.Writer,
		verifier: opts.Verifier,
	}, nil
}

// Read proves one typed arc query under a caller-supplied root.
func (e *Engine) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if e == nil {
		return ReadResult{}, fmt.Errorf("MALT engine is nil")
	}
	if !req.Root.Defined() {
		return ReadResult{}, fmt.Errorf("trusted root is undefined")
	}
	if err := req.Query.Validate(); err != nil {
		return ReadResult{}, err
	}

	switch req.Query.Kind {
	case QueryMapKey:
		return e.readMap(ctx, req)
	case QueryListIndex:
		return e.readListIndex(ctx, req)
	case QueryListRange:
		return e.readListRange(ctx, req)
	default:
		return ReadResult{}, fmt.Errorf("%w: unsupported kind %q", ErrInvalidQuery, req.Query.Kind)
	}
}

func (e *Engine) readMap(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if e.maps == nil {
		return ReadResult{}, fmt.Errorf("%w: map reader", ErrCapabilityUnavailable)
	}
	binding, proof, err := e.maps.Prove(ctx, e.scope, req.Root, req.Query.Key)
	if err != nil {
		return ReadResult{}, err
	}
	if !binding.Present || !binding.Value.Defined() {
		return ReadResult{}, ErrQueryNotFound
	}
	kind := prooflist.KindMapStep
	if req.Query.Key == arcset.PayloadPath {
		kind = prooflist.KindPayloadBinding
	}
	step := prooflist.Step{
		Kind:            kind,
		From:            req.Root,
		Query:           req.Query.String(),
		Coordinate:      req.Query.Key.String(),
		Path:            req.Query.Key.String(),
		Target:          binding.Value,
		EvidenceKind:    "structure",
		EvidenceBackend: "map",
		Proof:           cloneProof(proof),
	}
	return resultFromStep(req, step, nil), nil
}

func (e *Engine) readListIndex(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if e.lists == nil {
		return ReadResult{}, fmt.Errorf("%w: list reader", ErrCapabilityUnavailable)
	}
	result, proof, err := e.lists.Prove(ctx, e.scope, req.Root, req.Query.Index)
	if err != nil {
		return ReadResult{}, err
	}
	if !result.Key.Defined() {
		return ReadResult{}, ErrQueryNotFound
	}
	index := req.Query.Index
	length := result.Length
	step := prooflist.Step{
		Kind:            prooflist.KindListIndex,
		From:            req.Root,
		Query:           req.Query.String(),
		Coordinate:      fmt.Sprintf("%d", index),
		Index:           &index,
		Length:          &length,
		Target:          result.Key,
		EvidenceKind:    "structure",
		EvidenceBackend: "list",
		Proof:           cloneProof(proof),
	}
	return resultFromStep(req, step, nil), nil
}

func (e *Engine) readListRange(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if e.lists == nil {
		return ReadResult{}, fmt.Errorf("%w: list reader", ErrCapabilityUnavailable)
	}
	measured, ok := e.lists.(MeasuredListReader)
	if !ok {
		return ReadResult{}, fmt.Errorf("%w: measured list reader", ErrCapabilityUnavailable)
	}
	rangeResult, proof, err := measured.ProveRange(ctx, e.scope, req.Root, req.Query.Start, req.Query.End)
	if err != nil {
		return ReadResult{}, err
	}
	start := req.Query.Start
	end := cloneUint64(req.Query.End)
	childCount := rangeResult.Metadata.ChildCount
	totalSize := rangeResult.Metadata.TotalSize
	chunkSize := rangeResult.Metadata.ChunkSize
	step := prooflist.Step{
		Kind:            prooflist.KindListRange,
		From:            req.Root,
		Query:           req.Query.String(),
		Coordinate:      req.Query.String(),
		Start:           &start,
		End:             end,
		ChildCount:      &childCount,
		TotalSize:       &totalSize,
		ChunkSize:       &chunkSize,
		Target:          req.Root,
		Segments:        slices.Clone(rangeResult.Segments),
		EvidenceKind:    "structure",
		EvidenceBackend: "measured_list",
		Proof:           cloneProof(proof),
	}
	return resultFromStep(req, step, rangeResult.Segments), nil
}

func resultFromStep(req ReadRequest, step prooflist.Step, segments []cid.Cid) ReadResult {
	return ReadResult{
		Target:   step.Target,
		Segments: slices.Clone(segments),
		ProofList: prooflist.ProofList{
			Root:  req.Root,
			Query: req.Query.String(),
			Steps: []prooflist.Step{step},
		},
	}
}

// Apply applies a semantic mutation through the configured execution plane.
// Runtime scope is supplied by Engine and is not part of the canonical request.
func (e *Engine) Apply(ctx context.Context, mut Mutation) (WriteResult, error) {
	if e == nil {
		return WriteResult{}, fmt.Errorf("MALT engine is nil")
	}
	if e.writer == nil {
		return WriteResult{}, fmt.Errorf("%w: mutation applier", ErrCapabilityUnavailable)
	}
	return e.writer.Apply(ctx, e.scope, mut)
}

// VerifyRead binds the caller's trusted root, typed query, returned target, and
// ProofList before delegating cryptographic checks to a portable verifier.
func (e *Engine) VerifyRead(ctx context.Context, req ReadRequest, result ReadResult) error {
	if e == nil || e.verifier == nil {
		return fmt.Errorf("portable MALT verifier is unavailable")
	}
	return VerifyRead(ctx, req, result, e.verifier)
}

// VerifyRead validates the complete verifier-facing read contract.
func VerifyRead(ctx context.Context, req ReadRequest, result ReadResult, verifier ProofVerifier) error {
	if verifier == nil {
		return fmt.Errorf("portable MALT verifier is nil")
	}
	if !req.Root.Defined() {
		return fmt.Errorf("trusted root is undefined")
	}
	if err := req.Query.Validate(); err != nil {
		return err
	}
	if !result.ProofList.Root.Equals(req.Root) {
		return fmt.Errorf("ProofList root does not match trusted root")
	}
	if result.ProofList.Query != req.Query.String() {
		return fmt.Errorf("ProofList query %q does not match request %q", result.ProofList.Query, req.Query.String())
	}
	lastTarget, err := result.ProofList.LastStepTarget()
	if err != nil {
		return err
	}
	if err := validateQueryProofStep(req.Query, result.ProofList.Steps); err != nil {
		return err
	}
	if !result.Target.Defined() || !lastTarget.Equals(result.Target) {
		return fmt.Errorf("ProofList target does not match read result")
	}
	if req.Query.Kind == QueryListRange {
		last := result.ProofList.Steps[len(result.ProofList.Steps)-1]
		if !slices.EqualFunc(last.Segments, result.Segments, func(a, b cid.Cid) bool { return a.Equals(b) }) {
			return fmt.Errorf("ProofList segments do not match read result")
		}
	} else if len(result.Segments) != 0 {
		return fmt.Errorf("read result segments are only valid for list-range queries")
	}
	valid, err := verifier.VerifyProofList(ctx, result.ProofList)
	if err != nil {
		return err
	}
	if !valid {
		return ErrVerifierRejected
	}
	return nil
}

func validateQueryProofStep(query Query, steps []prooflist.Step) error {
	if len(steps) != 1 {
		return fmt.Errorf("typed arc query requires exactly one ProofList step, got %d", len(steps))
	}
	step := steps[0]
	switch query.Kind {
	case QueryMapKey:
		wantKind := prooflist.KindMapStep
		if query.Key == arcset.PayloadPath {
			wantKind = prooflist.KindPayloadBinding
		}
		if step.Kind != wantKind {
			return fmt.Errorf("map query proof kind %q does not match %q", step.Kind, wantKind)
		}
		if arcset.CanonicalizePath(step.Path) != query.Key {
			return fmt.Errorf("map query proof path %q does not match %q", step.Path, query.Key)
		}
	case QueryListIndex:
		if step.Kind != prooflist.KindListIndex || step.Index == nil || *step.Index != query.Index {
			return fmt.Errorf("list-index proof does not match index %d", query.Index)
		}
	case QueryListRange:
		if step.Kind != prooflist.KindListRange || step.Start == nil || *step.Start != query.Start || !equalOptionalUint64(step.End, query.End) {
			return fmt.Errorf("list-range proof does not match query bounds")
		}
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidQuery, query.Kind)
	}
	return nil
}

func equalOptionalUint64(a, b *uint64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func cloneProof(proof structure.Proof) []byte {
	return slices.Clone([]byte(proof))
}

// IsQueryNotFound reports whether err represents an unauthenticated or absent
// query target.
func IsQueryNotFound(err error) bool {
	return errors.Is(err, ErrQueryNotFound)
}
