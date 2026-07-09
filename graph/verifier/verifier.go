// Package verifier verifies verifier-facing ProofList artifacts against a
// caller-supplied graph runtime.
package verifier

import (
	"context"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/evidence"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	structure "github.com/dewebprotocol/malt/auth/semantic"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/querypath"
	"github.com/dewebprotocol/malt/graph/resolver"
)

// Runtime is the minimal graph runtime surface needed to verify ProofList
// evidence. It deliberately excludes server, daemon, head-publication, and
// gateway policy concerns.
type Runtime interface {
	Resolver() graph.Resolver
	Semantic() mapping.Semantics
	ListSemantic() list.Semantics
}

// Verifier verifies ProofList evidence against a runtime's resolver and
// semantic implementations.
type Verifier struct {
	runtime Runtime
}

// New creates a verifier over runtime.
func New(runtime Runtime) *Verifier {
	return &Verifier{runtime: runtime}
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

// VerifyProofList verifies the ordered ProofList structure, query binding, and
// per-step evidence against the verifier runtime.
func (v *Verifier) VerifyProofList(ctx context.Context, pl prooflist.ProofList) (bool, error) {
	if v == nil || v.runtime == nil {
		return false, fmt.Errorf("verifier runtime is nil")
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		return false, err
	}
	var verifiedPath proofListVerifiedPath
	for i, step := range pl.Steps {
		ok, err := v.verifyStep(ctx, i, step)
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
	cleanQuery, err := querypath.CanonicalizeQueryPath(pl.Query)
	if err != nil {
		return fmt.Errorf("invalid prooflist query path: %w", err)
	}
	want := arcset.CanonicalizePath(cleanQuery).String()
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

func (v *Verifier) verifyStep(ctx context.Context, index int, step prooflist.Step) (bool, error) {
	switch step.EvidenceKind {
	case "explicit":
		ev, err := decodeEvidence(step.EvidenceKind, step.Evidence)
		if err != nil {
			return false, fmt.Errorf("invalid evidence at step %d: %w", index, err)
		}
		return v.runtime.Resolver().VerifyTranscript(ctx, step.From, &resolver.Transcript{Steps: []resolver.StepEvidence{{
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
			return authverifier.VerifyProofListListStructure(v.runtime.ListSemantic(), step, index)
		default:
			return false, fmt.Errorf("prooflist step %d has unsupported structure evidence backend %q", index, step.EvidenceBackend)
		}
	default:
		return false, fmt.Errorf("prooflist step %d has unsupported evidence labels %q/%q", index, step.EvidenceKind, step.EvidenceBackend)
	}
}
