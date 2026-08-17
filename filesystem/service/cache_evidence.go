package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/dewebprotocol/malt-client/cache"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/protocol"
)

const cacheEvidenceProfile = "malt.filesystem.path-proof/v1"

type pathProofEvidence struct {
	Version         int               `json:"version"`
	DatasetID       string            `json:"dataset_id"`
	Branch          string            `json:"branch"`
	Revision        uint64            `json:"revision"`
	EncryptionEpoch uint32            `json:"encryption_epoch"`
	Path            string            `json:"path"`
	Resolution      unixfs.Resolution `json:"resolution"`
}

func marshalCacheEvidence(view View, path string, stat *unixfs.Stat) ([]byte, error) {
	resolution := payloadResolution(stat)
	return json.Marshal(pathProofEvidence{
		Version: 1, DatasetID: view.DatasetID, Branch: view.Branch,
		Revision: view.Revision, EncryptionEpoch: view.EncryptionEpoch,
		Path: path, Resolution: resolution,
	})
}

func newCacheProofVerifier(local unixfs.LocalVerifier, view View, path string, stat *unixfs.Stat) (cache.ProofVerifier, error) {
	if local == nil {
		return nil, fmt.Errorf("filesystem local verifier is nil")
	}
	expected := payloadResolution(stat)
	return cache.ProofVerifierFunc(func(ctx context.Context, binding cache.Binding, evidence cache.VerificationEvidence) error {
		if evidence.Profile != cacheEvidenceProfile {
			return fmt.Errorf("unsupported filesystem cache evidence profile %q", evidence.Profile)
		}
		if err := strictjson.ValidateUnicode(evidence.Evidence); err != nil {
			return fmt.Errorf("decode filesystem cache evidence: %w", err)
		}
		decoder := json.NewDecoder(bytes.NewReader(evidence.Evidence))
		decoder.DisallowUnknownFields()
		var stored pathProofEvidence
		if err := decoder.Decode(&stored); err != nil {
			return fmt.Errorf("decode filesystem cache evidence: %w", err)
		}
		if err := rejectTrailingJSON(decoder); err != nil {
			return err
		}
		if stored.Version != 1 || stored.DatasetID != binding.DatasetID || stored.Branch != binding.Branch ||
			stored.Revision != binding.Revision || stored.EncryptionEpoch != binding.EncryptionEpoch || stored.Path != path {
			return fmt.Errorf("filesystem cache evidence does not match the selected view")
		}
		if stored.Resolution.Request.Root != binding.Root.String() ||
			stored.Resolution.Result.Target != binding.CID.String() || !stored.Resolution.Target.Equals(binding.CID) ||
			stored.Resolution.Request.Root != expected.Request.Root ||
			!slices.Equal(stored.Resolution.Request.Segments, expected.Request.Segments) {
			return fmt.Errorf("filesystem cache evidence does not match the payload binding")
		}
		if err := local.VerifyResolve(ctx, protocol.ResolveVerification{
			Request: stored.Resolution.Request, Result: stored.Resolution.Result,
		}); err != nil {
			return fmt.Errorf("verify cached filesystem path proof: %w", err)
		}
		return nil
	}), nil
}

func payloadResolution(stat *unixfs.Stat) unixfs.Resolution {
	if stat.PayloadBinding != nil {
		return *stat.PayloadBinding
	}
	return stat.Resolution
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode filesystem cache evidence: trailing JSON value")
		}
		return fmt.Errorf("decode filesystem cache evidence: %w", err)
	}
	return nil
}
