package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
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
	if err := validateRequiredJSONShape(data, reflect.TypeFor[mapProofRequestJSONShape]()); err != nil {
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
	presence := make(map[string]struct{})
	if err := validateRequiredJSONShapeWithPresence(data, reflect.TypeFor[mapProofResultJSONShape](), presence); err != nil {
		return MapProofResult{}, fmt.Errorf("decode map-proof result: %w", err)
	}
	if err := validateMapProofTargetPresence(value.Present, presence, "$.target"); err != nil {
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
	presence := make(map[string]struct{})
	if err := validateRequiredJSONShapeWithPresence(data, reflect.TypeFor[mapProofVerificationJSONShape](), presence); err != nil {
		return MapProofVerification{}, fmt.Errorf("decode map-proof verification: %w", err)
	}
	if err := validateMapProofTargetPresence(value.Result.Present, presence, "$.result.target"); err != nil {
		return MapProofVerification{}, fmt.Errorf("decode map-proof verification: %w", err)
	}
	if err := value.Validate(); err != nil {
		return MapProofVerification{}, err
	}
	return value, nil
}

type mapProofRequestJSONShape struct {
	Profile string   `json:"profile"`
	Root    string   `json:"root"`
	Key     []string `json:"key"`
}

type mapProofResultJSONShape struct {
	Profile   string             `json:"profile"`
	Present   bool               `json:"present"`
	Target    string             `json:"target,omitempty"`
	ProofList proofListJSONShape `json:"prooflist"`
}

type proofListJSONShape struct {
	Root  json.RawMessage      `json:"root"`
	Query string               `json:"query,omitempty"`
	Steps []proofStepJSONShape `json:"steps"`
}

type proofStepJSONShape struct {
	Kind            string                 `json:"kind"`
	From            json.RawMessage        `json:"from"`
	Query           string                 `json:"query,omitempty"`
	Coordinate      string                 `json:"coordinate,omitempty"`
	Path            string                 `json:"path,omitempty"`
	Index           *uint64                `json:"index,omitempty"`
	Length          *uint64                `json:"length,omitempty"`
	Start           *uint64                `json:"start,omitempty"`
	End             *uint64                `json:"end,omitempty"`
	ChildCount      *uint64                `json:"child_count,omitempty"`
	TotalSize       *uint64                `json:"total_size,omitempty"`
	ChunkSize       *uint64                `json:"chunk_size,omitempty"`
	Target          nullableJSONRawMessage `json:"target"`
	Segments        []json.RawMessage      `json:"segments,omitempty"`
	EvidenceKind    string                 `json:"evidence_kind,omitempty"`
	EvidenceBackend string                 `json:"evidence_backend,omitempty"`
	Evidence        string                 `json:"evidence,omitempty"`
	Proof           string                 `json:"proof,omitempty"`
}

type nullableJSONRawMessage json.RawMessage

type mapProofVerificationJSONShape struct {
	Request mapProofRequestJSONShape `json:"request"`
	Result  mapProofResultJSONShape  `json:"result"`
}

func validateMapProofTargetPresence(present bool, presence map[string]struct{}, path string) error {
	_, carried := presence[path]
	if present && !carried {
		return fmt.Errorf("%s is required for a present map-proof result", path)
	}
	if !present && carried {
		return fmt.Errorf("%s must be omitted for an absent map-proof result", path)
	}
	return nil
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
