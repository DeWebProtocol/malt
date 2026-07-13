package server

import (
	"errors"
	"net/http"

	malt "github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/execution"
	"github.com/dewebprotocol/malt/protocol"
)

// handleResolveContract executes the current operation-specific resolve
// contract. The returned ProofList is untrusted evidence; callers retain the
// request and verify the pair locally.
func (s *Server) handleResolveContract(w http.ResponseWriter, r *http.Request) {
	var wireRequest protocol.ResolveRequest
	if err := s.decodeJSONBody(w, r, &wireRequest); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	request, err := wireRequest.Core()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	executor, err := s.protocolExecutor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolved, err := executor.Resolve(r.Context(), request)
	if err != nil {
		writeError(w, resolvePathStatus(err), err.Error())
		return
	}
	result, err := protocol.NewResolveResult(resolved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.RecordProofList(result.ProofList)
	writeJSON(w, http.StatusOK, result)
}

// handleReadContract executes one primitive map/list read. Multi-hop
// application traversal remains a resolve operation, not an implicit read.
func (s *Server) handleReadContract(w http.ResponseWriter, r *http.Request) {
	var wireRequest protocol.ReadRequest
	if err := s.decodeJSONBody(w, r, &wireRequest); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	request, err := wireRequest.Core()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	executor, err := s.protocolExecutor(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	readResult, err := executor.Read(r.Context(), request)
	if err != nil {
		status := http.StatusInternalServerError
		if malt.IsQueryNotFound(err) || errors.Is(err, mapping.ErrPathNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	result, err := protocol.NewReadResult(readResult)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.RecordProofList(result.ProofList)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleVerifyResolveContract(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Malt-Verification-Role", "diagnostic")
	var value protocol.ResolveVerification
	if err := s.decodeJSONBody(w, r, &value); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if err := value.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	verifier, err := s.verifierCache.load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	request, _ := value.Request.Core()
	result, _ := value.Result.Core()
	err = malt.VerifyResolve(r.Context(), request, result, verifier)
	writeProtocolVerificationResult(w, protocol.ResolveProfile, err)
}

func (s *Server) handleVerifyReadContract(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Malt-Verification-Role", "diagnostic")
	var value protocol.ReadVerification
	if err := s.decodeJSONBody(w, r, &value); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	if err := value.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	verifier, err := s.verifierCache.load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	request, _ := value.Request.Core()
	result, _ := value.Result.Core()
	err = malt.VerifyRead(r.Context(), request, result, verifier)
	writeProtocolVerificationResult(w, protocol.ReadProfile, err)
}

func (s *Server) protocolExecutor(r *http.Request) (*execution.Executor, error) {
	svc, err := s.graphService(r.Context())
	if err != nil {
		return nil, err
	}
	return execution.NewExecutor(execution.Options{
		Scope:    svc.runtime.Namespace(),
		Resolver: svc.runtime,
		Maps:     svc.runtime.Semantic(),
		Lists:    svc.runtime.ListSemantic(),
		Writer:   svc.runtime.Writer(),
	})
}

func writeProtocolVerificationResult(w http.ResponseWriter, profile string, err error) {
	result := protocol.VerificationResult{Profile: profile, Valid: err == nil}
	if err != nil {
		result.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, result)
}
