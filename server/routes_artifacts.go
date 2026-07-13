package server

import (
	"errors"
	"net/http"

	malt "github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/artifact"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
	"github.com/dewebprotocol/malt/execution"
	cid "github.com/ipfs/go-cid"
)

func (s *Server) handleArtifactResolve(w http.ResponseWriter, r *http.Request) {
	var req artifact.ResolveRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	root, _ := cid.Parse(req.Root)
	path, _ := malt.NewSegmentPath(req.Segments)
	svc, err := s.graphService(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolved, err := svc.runtime.Resolver().ResolveKey(r.Context(), root, path.String())
	if err != nil {
		writeError(w, resolvePathStatus(err), err.Error())
		return
	}
	if !resolved.RemainingPath.IsEmpty() || !resolved.Target.Defined() {
		writeError(w, http.StatusNotFound, "segment path was not fully resolved")
		return
	}
	pl, err := svc.ProofList(root, path.String(), resolved.Transcript)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result, err := artifact.NewResolveArtifact(req, resolved.Target, *pl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.RecordProofList(result.ProofList)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleArtifactProve(w http.ResponseWriter, r *http.Request) {
	var req artifact.ProveRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	root, _ := cid.Parse(req.Root)
	query, _ := req.Query.Core()

	svc, err := s.graphService(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	executor, err := execution.NewExecutor(execution.Options{
		Scope:  svc.runtime.Namespace(),
		Maps:   svc.runtime.Semantic(),
		Lists:  svc.runtime.ListSemantic(),
		Writer: svc.runtime.Writer(),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	readResult, err := executor.Read(r.Context(), malt.ReadRequest{Root: root, Query: query})
	if err != nil {
		status := http.StatusInternalServerError
		if malt.IsQueryNotFound(err) || errors.Is(err, mapping.ErrPathNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	result, err := artifact.NewProveArtifact(req, readResult)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.RecordProofList(result.ProofList)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleArtifactVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Malt-Verification-Role", "diagnostic")
	var req artifact.VerifyRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	verifier, err := authverifier.NewDefault()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := artifact.Verify(r.Context(), req, verifier); err != nil {
		writeJSON(w, http.StatusOK, artifact.VerifyResult{Profile: artifact.Profile, Valid: false})
		return
	}
	writeJSON(w, http.StatusOK, artifact.VerifyResult{Profile: artifact.Profile, Valid: true})
}
