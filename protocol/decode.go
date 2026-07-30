package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// MaxVerificationJSONBytes bounds portable verifier inputs before JSON
// decoding. It matches the current transport body ceiling while keeping the
// protocol decoder independent from HTTP and WASM adapters.
const MaxVerificationJSONBytes = 96 << 20

// DecodeResolveVerification strictly decodes and validates one portable
// resolve request/result pair. Unknown fields and trailing JSON values are
// rejected so every language adapter observes the published schema boundary.
func DecodeResolveVerification(data []byte) (ResolveVerification, error) {
	var value ResolveVerification
	if err := decodeVerificationJSON(data, &value); err != nil {
		return ResolveVerification{}, fmt.Errorf("decode resolve verification: %w", err)
	}
	if err := value.Validate(); err != nil {
		return ResolveVerification{}, err
	}
	return value, nil
}

// DecodeReadVerification strictly decodes and validates one portable primitive
// read request/result pair.
func DecodeReadVerification(data []byte) (ReadVerification, error) {
	var value ReadVerification
	if err := decodeVerificationJSON(data, &value); err != nil {
		return ReadVerification{}, fmt.Errorf("decode read verification: %w", err)
	}
	if err := value.Validate(); err != nil {
		return ReadVerification{}, err
	}
	return value, nil
}

// DecodeMapProofRequest strictly decodes one caller-selected map-proof request.
func DecodeMapProofRequest(data []byte) (MapProofRequest, error) {
	var value MapProofRequest
	if err := decodeVerificationJSON(data, &value); err != nil {
		return MapProofRequest{}, fmt.Errorf("decode map-proof request: %w", err)
	}
	if err := value.Validate(); err != nil {
		return MapProofRequest{}, err
	}
	return value, nil
}

// DecodeMapProofResult strictly decodes one untrusted map-proof result.
func DecodeMapProofResult(data []byte) (MapProofResult, error) {
	var value MapProofResult
	if err := decodeVerificationJSON(data, &value); err != nil {
		return MapProofResult{}, fmt.Errorf("decode map-proof result: %w", err)
	}
	if err := value.Validate(); err != nil {
		return MapProofResult{}, err
	}
	return value, nil
}

// DecodeMapProofVerification strictly decodes one map-proof request/result pair.
func DecodeMapProofVerification(data []byte) (MapProofVerification, error) {
	var value MapProofVerification
	if err := decodeVerificationJSON(data, &value); err != nil {
		return MapProofVerification{}, fmt.Errorf("decode map-proof verification: %w", err)
	}
	if err := value.Validate(); err != nil {
		return MapProofVerification{}, err
	}
	return value, nil
}

func decodeVerificationJSON(data []byte, target any) error {
	if len(data) == 0 {
		return fmt.Errorf("verification JSON is empty")
	}
	if len(data) > MaxVerificationJSONBytes {
		return fmt.Errorf("verification JSON exceeds %d bytes", MaxVerificationJSONBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("unexpected trailing JSON: %w", err)
	}
	return nil
}
