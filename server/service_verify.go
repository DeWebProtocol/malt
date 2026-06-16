package server

import (
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/evidence"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/auth/semantic"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/graph/querypath"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/layout/unixfs"
)

type proofVerifier struct {
	runtime runtimeGraph
}

type proofListVerifiedPath struct {
	parts             []string
	hasPayloadBinding bool
}

func (p *proofListVerifiedPath) addStep(step prooflist.Step) error {
	path := arcset.CanonicalizePath(step.Path).String()
	if path == "" || step.EvidenceKind == "structure" && (step.EvidenceBackend == "list" || step.EvidenceBackend == "measured_list") {
		return nil
	}
	if p.hasPayloadBinding {
		return fmt.Errorf("prooflist traversal step %q appears after terminal @payload binding", path)
	}
	if path == "@payload" {
		p.hasPayloadBinding = true
		return nil
	}
	p.parts = append(p.parts, path)
	return nil
}

func (p proofListVerifiedPath) logicalQueryPath() string {
	return strings.Join(p.parts, "/")
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

func decodeEvidence(kind string, payload []byte) (evidence.Evidence, error) {
	switch kind {
	case "explicit":
		return evidence.NewExplicitEvidence(payload), nil
	default:
		return nil, fmt.Errorf("unknown evidence kind %q", kind)
	}
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
	case "explicit":
		ev, err := decodeEvidence(step.EvidenceKind, step.Evidence)
		if err != nil {
			return false, fmt.Errorf("invalid evidence at step %d: %w", index, err)
		}
		return v.runtime.Resolver().VerifyTranscript(step.From, &resolver.Transcript{Steps: []resolver.StepEvidence{{
			Path:     arcset.CanonicalizePath(step.Path),
			Target:   step.Target,
			Evidence: ev,
		}}})
	case "implicit", "hamt":
		return false, fmt.Errorf("prooflist step %d uses legacy evidence kind %q; server verifier supports explicit evidence only", index, step.EvidenceKind)
	case "structure":
		switch step.EvidenceBackend {
		case "map":
			key := arcset.CanonicalizePath(step.Path)
			return v.runtime.Semantic().Verify(step.From, key, mapping.Binding{Value: step.Target, Present: true}, structure.Proof(step.Proof))
		case "list", "measured_list":
			return unixfs.VerifyProofListListStructure(v.runtime.ListSemantic(), step, index)
		default:
			return false, fmt.Errorf("prooflist step %d has unsupported structure evidence backend %q", index, step.EvidenceBackend)
		}
	default:
		return false, fmt.Errorf("prooflist step %d has unsupported evidence labels %q/%q", index, step.EvidenceKind, step.EvidenceBackend)
	}
}
