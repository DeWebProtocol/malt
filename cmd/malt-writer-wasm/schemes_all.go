//go:build !writer_kzg && !writer_ipa

package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func compiledBackend() string { return "all" }

func newComputer(backend string) (*computer, error) {
	schemes := make(map[maltcid.BackendKind]commitment.IndexCommitment)
	if backend == "all" || backend == string(maltcid.BackendKindKZG) {
		scheme, err := kzg.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("initialize KZG writer: %w", err)
		}
		schemes[maltcid.BackendKindKZG] = scheme
	}
	if backend == "all" || backend == string(maltcid.BackendKindIPA) {
		scheme, err := ipa.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("initialize IPA writer: %w", err)
		}
		schemes[maltcid.BackendKindIPA] = scheme
	}
	if len(schemes) == 0 {
		return nil, fmt.Errorf("unsupported writer backend %q", backend)
	}
	return &computer{schemes: schemes}, nil
}
