//go:build writer_ipa && !writer_kzg

package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func compiledBackend() maltcid.BackendKind {
	return maltcid.BackendKindIPA
}

func newCompiledCommitmentComputer(backend maltcid.BackendKind) (*commitmentComputer, error) {
	if backend != maltcid.BackendKindIPA {
		return nil, fmt.Errorf("compiled commitment backend is IPA, not %q", backend)
	}
	scheme, err := ipa.NewScheme()
	if err != nil {
		return nil, fmt.Errorf("initialize IPA commitment backend: %w", err)
	}
	return newCommitmentComputer(backend, scheme)
}
