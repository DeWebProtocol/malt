// Package verifier provides the client-side MALT verification facade. It is
// deterministic, performs no network or storage I/O, and treats the root,
// operation, query, and optional target in a request as caller-selected input.
package verifier

import (
	"context"
	"fmt"

	malt "github.com/dewebprotocol/malt"
	"github.com/dewebprotocol/malt/artifact"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
	"github.com/dewebprotocol/malt/protocol"
)

type Verifier struct {
	proofs *authverifier.Verifier
}

// VerifyResolve locally verifies a transport-neutral resolve request/result
// pair. The request is caller-selected; the result and ProofList are untrusted.
func (v *Verifier) VerifyResolve(ctx context.Context, value protocol.ResolveVerification) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if v == nil || v.proofs == nil {
		return fmt.Errorf("client verifier is nil")
	}
	request, _ := value.Request.Core()
	result, _ := value.Result.Core()
	return malt.VerifyResolve(ctx, request, result, v.proofs)
}

// VerifyRead locally verifies one transport-neutral primitive read
// request/result pair.
func (v *Verifier) VerifyRead(ctx context.Context, value protocol.ReadVerification) error {
	if err := value.Validate(); err != nil {
		return err
	}
	if v == nil || v.proofs == nil {
		return fmt.Errorf("client verifier is nil")
	}
	request, _ := value.Request.Core()
	result, _ := value.Result.Core()
	return malt.VerifyRead(ctx, request, result, v.proofs)
}

func NewDefault() (*Verifier, error) {
	proofs, err := authverifier.NewDefault()
	if err != nil {
		return nil, err
	}
	return &Verifier{proofs: proofs}, nil
}

// Verify verifies the frozen malt.artifact/v0alpha2 compatibility envelope.
// New callers should use VerifyResolve or VerifyRead.
func (v *Verifier) Verify(ctx context.Context, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if v == nil || v.proofs == nil {
		return fmt.Errorf("client verifier is nil")
	}
	return artifact.Verify(ctx, artifact.VerifyRequest{Profile: artifact.Profile, Artifact: request.Artifact}, v.proofs)
}

// VerifyProofList verifies bare evidence locally. Prefer VerifyResolve or
// VerifyRead so caller-selected inputs are bound to the untrusted result.
func (v *Verifier) VerifyProofList(ctx context.Context, value prooflist.ProofList) (bool, error) {
	if v == nil || v.proofs == nil {
		return false, fmt.Errorf("client verifier is nil")
	}
	return v.proofs.VerifyProofList(ctx, value)
}
