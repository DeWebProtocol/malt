//go:build !writer_kzg && !writer_ipa

package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func newCompiledCommitmentComputer(backend maltcid.BackendKind) (*commitmentComputer, error) {
	var (
		scheme commitment.IndexCommitment
		err    error
	)
	switch backend {
	case maltcid.BackendKindKZG:
		scheme, err = kzg.NewScheme()
	case maltcid.BackendKindIPA:
		scheme, err = ipa.NewScheme()
	default:
		return nil, fmt.Errorf("unsupported commitment backend %q", backend)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize %s commitment backend: %w", backend, err)
	}
	return newCommitmentComputer(backend, scheme)
}
