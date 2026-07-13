// Package verifier provides the client-side MALT verification facade. It is
// deterministic, performs no network or storage I/O, and treats the root,
// operation, query, and optional target in a request as caller-selected input.
package verifier

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/artifact"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
)

type Verifier struct {
	proofs *authverifier.Verifier
}

func NewDefault() (*Verifier, error) {
	proofs, err := authverifier.NewDefault()
	if err != nil {
		return nil, err
	}
	return &Verifier{proofs: proofs}, nil
}

// Verify verifies the local-verifier envelope and binds the artifact to the
// caller-selected trusted root, operation, query, and optional target before
// checking cryptographic evidence.
func (v *Verifier) Verify(ctx context.Context, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if v == nil || v.proofs == nil {
		return fmt.Errorf("client verifier is nil")
	}
	return artifact.Verify(ctx, artifact.VerifyRequest{Profile: artifact.Profile, Artifact: request.Artifact}, v.proofs)
}

// VerifyProofList verifies a bare ProofList locally. Prefer Verify when the
// profiled envelope is available because it also binds caller expectations.
func (v *Verifier) VerifyProofList(ctx context.Context, value prooflist.ProofList) (bool, error) {
	if v == nil || v.proofs == nil {
		return false, fmt.Errorf("client verifier is nil")
	}
	return v.proofs.VerifyProofList(ctx, value)
}
