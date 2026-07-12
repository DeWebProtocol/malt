package artifact

import (
	"context"
	"fmt"

	malt "github.com/dewebprotocol/malt"
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
	lastTarget, err := req.Artifact.ProofList.LastStepTarget()
	if err != nil {
		return err
	}
	if !lastTarget.Equals(target) {
		return fmt.Errorf("artifact ProofList target does not match artifact target")
	}

	switch req.Artifact.Operation {
	case OperationResolve:
		path, _ := malt.NewSegmentPath(req.Artifact.Query.Segments)
		if req.Artifact.ProofList.Query != path.String() {
			return fmt.Errorf("artifact ProofList query %q does not match segment path %q", req.Artifact.ProofList.Query, path.String())
		}
		ok, err := verifier.VerifyProofList(ctx, req.Artifact.ProofList)
		if err != nil {
			return err
		}
		if !ok {
			return malt.ErrVerifierRejected
		}
		return nil
	case OperationProve:
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
