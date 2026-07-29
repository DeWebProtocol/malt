//go:build writer_kzg && !writer_ipa

package main

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func startupBackend() (string, error) {
	return string(maltcid.BackendKindKZG), nil
}

func newComputer(backend string) (*computer, error) {
	if backend != string(maltcid.BackendKindKZG) {
		return nil, fmt.Errorf("KZG writer does not support backend %q", backend)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		return nil, fmt.Errorf("initialize KZG writer: %w", err)
	}
	return &computer{schemes: map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindKZG: scheme,
	}}, nil
}
