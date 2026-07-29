//go:build writer_kzg && !writer_ipa

package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func compiledBackend() maltcid.BackendKind {
	return maltcid.BackendKindKZG
}

func newCompiledCommitmentComputer(backend maltcid.BackendKind) (*commitmentComputer, error) {
	if backend != maltcid.BackendKindKZG {
		return nil, fmt.Errorf("compiled commitment backend is KZG, not %q", backend)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		return nil, fmt.Errorf("initialize KZG commitment backend: %w", err)
	}
	return newCommitmentComputer(backend, scheme)
}
