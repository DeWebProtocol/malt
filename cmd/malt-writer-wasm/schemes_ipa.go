//go:build writer_ipa && !writer_kzg

package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func compiledBackend() string { return string(maltcid.BackendKindIPA) }

func newComputer(backend string) (*computer, error) {
	if backend != string(maltcid.BackendKindIPA) {
		return nil, fmt.Errorf("IPA writer does not support backend %q", backend)
	}
	scheme, err := ipa.NewScheme()
	if err != nil {
		return nil, fmt.Errorf("initialize IPA writer: %w", err)
	}
	return &computer{schemes: map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindIPA: scheme,
	}}, nil
}
