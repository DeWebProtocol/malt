//go:build !writer_kzg && !writer_ipa

package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

func startupBackend() (string, error) {
	return parseStartupBackend(os.Args[1:])
}

func newComputer(backend string) (*computer, error) {
	var (
		kind   maltcid.BackendKind
		scheme commitment.IndexCommitment
		err    error
	)
	switch backend {
	case string(maltcid.BackendKindKZG):
		kind = maltcid.BackendKindKZG
		scheme, err = kzg.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("initialize KZG writer: %w", err)
		}
	case string(maltcid.BackendKindIPA):
		kind = maltcid.BackendKindIPA
		scheme, err = ipa.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("initialize IPA writer: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported writer backend %q", backend)
	}
	return &computer{schemes: map[maltcid.BackendKind]commitment.IndexCommitment{
		kind: scheme,
	}}, nil
}
