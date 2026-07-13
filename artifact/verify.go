package artifact

import (
	"context"
	"fmt"
	"strings"

	malt "github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	cid "github.com/ipfs/go-cid"
)

// Verify validates the request/artifact envelope and all cryptographic
// evidence. Resolution is existential: it proves the returned ordered arc
// chain, not that the execution engine selected a unique or longest chain.
func Verify(ctx context.Context, req VerifyRequest, verifier malt.ProofVerifier) error {
	if verifier == nil {
		return fmt.Errorf("artifact verifier is nil")
	}
	if err := req.Validate(); err != nil {
		return err
	}

	root, _ := cid.Parse(req.Artifact.Root)
	target, _ := cid.Parse(req.Artifact.Target)
	if !req.Artifact.ProofList.Root.Equals(root) {
		return fmt.Errorf("artifact ProofList root does not match artifact root")
	}
	if req.Artifact.Operation == OperationResolve && len(req.Artifact.Query.Segments) == 0 {
		if len(req.Artifact.ProofList.Steps) != 0 || req.Artifact.ProofList.Query != "" {
			return fmt.Errorf("root identity artifact contains traversal evidence")
		}
		if !target.Equals(root) {
			return fmt.Errorf("root identity artifact target does not match root")
		}
		return nil
	}
	switch req.Artifact.Operation {
	case OperationResolve:
		lastTarget, err := req.Artifact.ProofList.LastStepTarget()
		if err != nil {
			return err
		}
		if !lastTarget.Equals(target) {
			return fmt.Errorf("artifact ProofList target does not match artifact target")
		}
		path, _ := malt.NewSegmentPath(req.Artifact.Query.Segments)
		if req.Artifact.ProofList.Query != path.String() {
			return fmt.Errorf("artifact ProofList query %q does not match segment path %q", req.Artifact.ProofList.Query, path.String())
		}
		return verifyProofList(ctx, verifier, req.Artifact.ProofList)
	case OperationResolvePayload:
		path, _ := malt.NewSegmentPath(req.Artifact.Query.Segments)
		if req.Artifact.ProofList.Query != path.String() {
			return fmt.Errorf("artifact ProofList query %q does not match segment path %q", req.Artifact.ProofList.Query, path.String())
		}
		payloadTarget, err := proofListPayloadTarget(req.Artifact.ProofList, path.String())
		if err != nil {
			return err
		}
		if !payloadTarget.Equals(target) {
			return fmt.Errorf("artifact payload-binding target does not match artifact target")
		}
		return verifyProofList(ctx, verifier, req.Artifact.ProofList)
	case OperationProve:
		lastTarget, err := req.Artifact.ProofList.LastStepTarget()
		if err != nil {
			return err
		}
		if !lastTarget.Equals(target) {
			return fmt.Errorf("artifact ProofList target does not match artifact target")
		}
		query, err := req.Artifact.Query.Core()
		if err != nil {
			return err
		}
		segments := make([]cid.Cid, len(req.Artifact.RangeSegments))
		for i, raw := range req.Artifact.RangeSegments {
			segments[i], _ = cid.Parse(raw)
		}
		return malt.VerifyRead(ctx, malt.ReadRequest{Root: root, Query: query}, malt.ReadResult{
			Target:    target,
			Segments:  segments,
			ProofList: req.Artifact.ProofList,
		}, verifier)
	default:
		return fmt.Errorf("unsupported artifact operation %q", req.Artifact.Operation)
	}
}

func proofListPayloadTarget(pl prooflist.ProofList, expectedPath string) (cid.Cid, error) {
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		return cid.Undef, err
	}
	var target cid.Cid
	var pathParts []string
	for i, step := range pl.Steps {
		if step.Kind != prooflist.KindPayloadBinding {
			if step.Kind != prooflist.KindListIndex && step.Kind != prooflist.KindListRange {
				if part := arcset.CanonicalizePath(step.Path).String(); part != "" {
					pathParts = append(pathParts, part)
				}
			}
			continue
		}
		if target.Defined() {
			return cid.Undef, fmt.Errorf("resolve_payload ProofList contains multiple payload-binding steps")
		}
		if step.Path != "@payload" {
			return cid.Undef, fmt.Errorf("resolve_payload ProofList step %d does not select @payload", i)
		}
		target = step.Target
	}
	if !target.Defined() {
		return cid.Undef, fmt.Errorf("resolve_payload ProofList has no payload-binding step")
	}
	if actualPath := strings.Join(pathParts, "/"); actualPath != expectedPath {
		return cid.Undef, fmt.Errorf("resolve_payload traversal path %q does not match segment path %q", actualPath, expectedPath)
	}
	return target, nil
}

func verifyProofList(ctx context.Context, verifier malt.ProofVerifier, pl prooflist.ProofList) error {
	ok, err := verifier.VerifyProofList(ctx, pl)
	if err != nil {
		return err
	}
	if !ok {
		return malt.ErrVerifierRejected
	}
	return nil
}
