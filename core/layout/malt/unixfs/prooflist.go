package unixfs

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/prooflist"
	cid "github.com/ipfs/go-cid"
)

// ProofListFromSteps converts UnixFS layout resolution steps into the
// verifier-facing ProofList schema. It is an adapter over existing layout
// proofs and does not change AddFile, Resolve, or ReadFile behavior.
func ProofListFromSteps(root cid.Cid, queriedPath string, steps []Step) (*prooflist.ProofList, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root is undefined")
	}

	pl := &prooflist.ProofList{
		Root:  root,
		Query: queriedPath,
		Steps: make([]prooflist.Step, 0, len(steps)),
	}
	for _, layoutStep := range steps {
		path := layoutStep.Path.String()
		pl.Steps = append(pl.Steps, prooflist.Step{
			Kind:            unixFSStepKind(path),
			From:            layoutStep.Root,
			Query:           queriedPath,
			Path:            path,
			Target:          layoutStep.Target,
			EvidenceKind:    "structure",
			EvidenceBackend: "map",
			Proof:           cloneProofBytes(layoutStep.Proof),
		})
	}
	return pl, nil
}

func unixFSStepKind(path string) prooflist.StepKind {
	if path == payloadPath.String() {
		return prooflist.KindPayloadBinding
	}
	return prooflist.KindMapStep
}

func cloneProofBytes(proof []byte) []byte {
	out := make([]byte, len(proof))
	copy(out, proof)
	return out
}
