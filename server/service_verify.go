package server

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/querypath"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	cid "github.com/ipfs/go-cid"
)

type proofVerifier struct {
	runtime graph.Runtime
}

func (v proofVerifier) VerifyProofList(pl prooflist.ProofList) (bool, error) {
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		return false, err
	}
	var verifiedPath proofListVerifiedPath
	for i, step := range pl.Steps {
		ok, err := v.verifyStep(i, step)
		if err != nil || !ok {
			return ok, err
		}
		if err := verifiedPath.addStep(step); err != nil {
			return false, err
		}
	}
	if err := validateProofListQuery(pl, verifiedPath); err != nil {
		return false, err
	}
	return true, nil
}

func validateProofListQuery(pl prooflist.ProofList, verifiedPath proofListVerifiedPath) error {
	want := arcset.CanonicalizePath(querypath.CanonicalizeQueryPath(pl.Query)).String()
	if want == "" {
		return nil
	}
	got := verifiedPath.logicalQueryPath()
	if got == want {
		return nil
	}
	if verifiedPath.hasPayloadBinding {
		payloadQuery := "@payload"
		if got != "" {
			payloadQuery = got + "/@payload"
		}
		if want == payloadQuery {
			return nil
		}
	}
	return fmt.Errorf("prooflist query %q does not match ordered traversal path %q", want, got)
}

func (v proofVerifier) verifyStep(index int, step prooflist.Step) (bool, error) {
	switch step.EvidenceKind {
	case "explicit", "implicit", "hamt":
		ev, err := decodeEvidence(step.EvidenceKind, step.Evidence)
		if err != nil {
			return false, fmt.Errorf("invalid evidence at step %d: %w", index, err)
		}
		return v.runtime.Resolver().VerifyTranscript(step.From, &resolver.Transcript{Steps: []resolver.StepEvidence{{
			Path:     arcset.CanonicalizePath(step.Path),
			Target:   step.Target,
			Evidence: ev,
		}}})
	case "structure":
		switch step.EvidenceBackend {
		case "map":
			key := arcset.CanonicalizePath(step.Path)
			return v.runtime.Semantic().Verify(step.From, key, mapping.Binding{Value: step.Target, Present: true}, structure.Proof(step.Proof))
		case "list":
			if step.Index == nil {
				return false, fmt.Errorf("prooflist step %d list index is missing", index)
			}
			if step.Length == nil {
				return false, fmt.Errorf("prooflist step %d list length is missing", index)
			}
			return v.runtime.ListSemantic().Verify(step.From, *step.Index, list.Query{Key: step.Target, Length: *step.Length}, structure.Proof(step.Proof))
		case "measured_list":
			if !step.Target.Equals(step.From) {
				return false, nil
			}
			if step.Start == nil {
				return false, fmt.Errorf("prooflist step %d list range start is missing", index)
			}
			if step.ChildCount == nil {
				return false, fmt.Errorf("prooflist step %d list range child count is missing", index)
			}
			if step.TotalSize == nil {
				return false, fmt.Errorf("prooflist step %d list range total size is missing", index)
			}
			if step.ChunkSize == nil {
				return false, fmt.Errorf("prooflist step %d list range chunk size is missing", index)
			}
			measured, ok := v.runtime.ListSemantic().(list.MeasuredSemantics)
			if !ok {
				return false, fmt.Errorf("prooflist step %d has measured list evidence but graph list semantic does not support measured ranges", index)
			}
			return measured.VerifyRange(step.From, *step.Start, step.End, list.RangeResult{
				Metadata: list.RangeMetadata{
					ChildCount: *step.ChildCount,
					TotalSize:  *step.TotalSize,
					ChunkSize:  *step.ChunkSize,
				},
				Segments: append([]cid.Cid(nil), step.Segments...),
			}, structure.Proof(step.Proof))
		default:
			return false, fmt.Errorf("prooflist step %d has unsupported structure evidence backend %q", index, step.EvidenceBackend)
		}
	default:
		return false, fmt.Errorf("prooflist step %d has unsupported evidence labels %q/%q", index, step.EvidenceKind, step.EvidenceBackend)
	}
}
