package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dewebprotocol/malt-client/cache"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
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

func newCacheProofVerifier(local *engine.Engine, view View, path string, stat *unixfs.Stat) (cache.ProofVerifier, error) {
	if local == nil {
		return nil, fmt.Errorf("filesystem local verifier is nil")
	}
	expected := payloadResolution(stat)
	if expected.Authentication == nil {
		return nil, fmt.Errorf("filesystem authentication evidence is missing")
	}
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

		wire := stored.Resolution.Authentication
		if wire == nil {
			return fmt.Errorf("cached typed proof is missing")
		}
		expectedJSON, _ := json.Marshal(expected.Authentication.Request)
		actualJSON, _ := json.Marshal(wire.Request)
		if !bytes.Equal(expectedJSON, actualJSON) || wire.Request.Root != binding.Root.String() || wire.Result.Resolved != binding.CID.String() || !stored.Resolution.Target.Equals(binding.CID) {
			return fmt.Errorf("cached typed proof changed selected query")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		valid, err := authentication.Verify(local, wire.Request, wire.Result)
		if err != nil {
			return err
		}
		if !valid {
			return fmt.Errorf("invalid cached typed proof")
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
