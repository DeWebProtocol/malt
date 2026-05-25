package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	svc, err := s.graphService(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid root CID: "+err.Error())
		return
	}
	s.serveResolve(w, r, svc, root, r.PathValue("path"))
}

func (s *Server) serveResolve(w http.ResponseWriter, r *http.Request, svc graphService, root cid.Cid, queryPath string) {
	resolved, err := s.resolvePath(r.Context(), svc, root, queryPath, !shouldOmitDefaultProof(r))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	addVaryHeader(w, "X-Malt-Proof")
	resp := &httpapi.ResolveResponse{Target: resolved.target.String()}
	if resolved.proofList != nil {
		resp.ProofList = resolved.proofList
		s.node.RecordProofList(*resolved.proofList)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) resolvePath(ctx context.Context, svc graphService, root cid.Cid, rawPath string, wantProof bool) (*pathResolution, error) {
	cleanPath, keyResult, err := svc.ResolveKey(root, rawPath)
	if err != nil {
		return nil, err
	}
	key := keyResult.Target

	stat, target, err := s.statForResolvedKey(ctx, svc.runtime, key)
	if err != nil {
		return nil, err
	}

	resolved := &pathResolution{
		queryPath: cleanPath,
		target:    target,
		stat:      stat,
	}
	if !wantProof {
		return resolved, nil
	}

	transcript := keyResult.Transcript
	if maltcid.SemanticKindOf(key) == maltcid.SemanticKindMap {
		payloadResult, err := svc.ResolveMapPayload(key)
		if err != nil {
			return nil, err
		}
		if !payloadResult.Target.Equals(target) {
			return nil, fmt.Errorf("%w: resolved target %s does not match projected target %s", resolver.ErrResolutionFailed, payloadResult.Target, target)
		}
		steps := append([]resolver.StepEvidence(nil), transcript.Steps...)
		steps = append(steps, payloadResult.Transcript.Steps...)
		transcript = &resolver.Transcript{Steps: steps}
	}

	pl, err := svc.ProofList(root, cleanPath, transcript)
	if err != nil {
		return nil, err
	}
	resolved.proofList = pl
	return resolved, nil
}
