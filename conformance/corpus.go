// Package conformance exposes the checked-in, language-neutral resolve/read
// verification corpus. The corpus is immutable protocol test data; producing
// runtime evidence remains the responsibility of the deterministic generator.
package conformance

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt/protocol"
)

const (
	// ResolveReadV1 is the independent conformance-corpus version. It is not a
	// replacement for the resolve/read wire profile identifiers.
	ResolveReadV1 = "malt.resolve-read.conformance/v1"

	OperationResolve = "resolve"
	OperationRead    = "read"

	BackendNone = "none"
	BackendKZG  = "kzg"
	BackendIPA  = "ipa"
)

const corpusPath = "resolve-read/v1/vectors.json"

//go:generate go run ../internal/conformancegen/cmd -out resolve-read/v1/vectors.json

//go:embed resolve-read/v1/*.json
var corpusFiles embed.FS

// Corpus is the stable, ordered envelope shared by every verifier adapter.
type Corpus struct {
	SchemaVersion string   `json:"schema_version"`
	Vectors       []Vector `json:"vectors"`
}

// Vector binds one exact serialized verification input to an accept/reject
// expectation. Verification remains raw so malformed field encodings can be
// tested without weakening the corpus envelope decoder.
type Vector struct {
	ID           string          `json:"id"`
	Operation    string          `json:"operation"`
	Backend      string          `json:"backend"`
	Category     string          `json:"category"`
	Verification json.RawMessage `json:"verification"`
	Expected     Expected        `json:"expected"`
}

// Expected records the portable outcome. Error strings are deliberately not
// conformance data because they are implementation-specific diagnostics.
type Expected struct {
	Valid bool `json:"valid"`
}

// UnmarshalJSON makes expected.valid required while retaining a convenient
// bool-facing API for corpus consumers.
func (e *Expected) UnmarshalJSON(data []byte) error {
	var wire struct {
		Valid *bool `json:"valid"`
	}
	if err := decodeStrict(data, &wire); err != nil {
		return err
	}
	if wire.Valid == nil {
		return fmt.Errorf("expected.valid is required")
	}
	e.Valid = *wire.Valid
	return nil
}

// Verifier is the portable resolve/read verification surface exercised by the
// corpus. sdk/verifier.Verifier satisfies this interface.
type Verifier interface {
	VerifyResolve(context.Context, protocol.ResolveVerification) error
	VerifyRead(context.Context, protocol.ReadVerification) error
}

// Load returns and validates the embedded v1 corpus.
func Load() (Corpus, error) {
	data, err := Bytes()
	if err != nil {
		return Corpus{}, err
	}
	var corpus Corpus
	if err := decodeStrict(data, &corpus); err != nil {
		return Corpus{}, fmt.Errorf("decode conformance corpus: %w", err)
	}
	if err := corpus.Validate(); err != nil {
		return Corpus{}, err
	}
	return corpus, nil
}

// Bytes returns a detached copy of the canonical checked-in corpus bytes.
func Bytes() ([]byte, error) {
	data, err := corpusFiles.ReadFile(corpusPath)
	if err != nil {
		return nil, fmt.Errorf("read embedded conformance corpus: %w", err)
	}
	return slices.Clone(data), nil
}

// Schema returns a detached copy of one checked-in conformance schema.
func Schema(name string) ([]byte, error) {
	if name != "corpus.schema.json" && name != "vector.schema.json" {
		return nil, fmt.Errorf("unknown conformance schema %q", name)
	}
	data, err := corpusFiles.ReadFile("resolve-read/v1/" + name)
	if err != nil {
		return nil, fmt.Errorf("read conformance schema %q: %w", name, err)
	}
	return slices.Clone(data), nil
}

// Validate checks corpus-level invariants that JSON Schema alone cannot make
// convenient for small consumers, including stable ID uniqueness.
func (c Corpus) Validate() error {
	if c.SchemaVersion != ResolveReadV1 {
		return fmt.Errorf("unsupported conformance schema version %q", c.SchemaVersion)
	}
	if len(c.Vectors) == 0 {
		return fmt.Errorf("conformance corpus has no vectors")
	}
	seen := make(map[string]struct{}, len(c.Vectors))
	for i, vector := range c.Vectors {
		if err := vector.Validate(); err != nil {
			return fmt.Errorf("vector %d: %w", i, err)
		}
		if _, exists := seen[vector.ID]; exists {
			return fmt.Errorf("duplicate conformance vector id %q", vector.ID)
		}
		seen[vector.ID] = struct{}{}
	}
	return nil
}

// Validate checks the stable envelope without interpreting the operation DTO.
func (v Vector) Validate() error {
	if !validVectorID(v.ID) {
		return fmt.Errorf("invalid vector id %q", v.ID)
	}
	if v.Operation != OperationResolve && v.Operation != OperationRead {
		return fmt.Errorf("vector %q has unsupported operation %q", v.ID, v.Operation)
	}
	if v.Backend != BackendNone && v.Backend != BackendKZG && v.Backend != BackendIPA {
		return fmt.Errorf("vector %q has unsupported backend %q", v.ID, v.Backend)
	}
	if v.Category == "" {
		return fmt.Errorf("vector %q has empty category", v.ID)
	}
	trimmed := bytes.TrimSpace(v.Verification)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return fmt.Errorf("vector %q verification is not a JSON object", v.ID)
	}
	return nil
}

func validVectorID(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	separator := true
	for _, r := range value {
		alphanumeric := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if alphanumeric {
			separator = false
			continue
		}
		if (r != '.' && r != '_' && r != '-') || separator {
			return false
		}
		separator = true
	}
	return !separator
}

// Accepted evaluates one vector through the same strict DTO decoders used by
// transport adapters. Any decode, shape, binding, or cryptographic error is a
// portable rejection; diagnostic error text is intentionally ignored.
func Accepted(ctx context.Context, verifier Verifier, vector Vector) bool {
	if verifier == nil {
		return false
	}
	switch vector.Operation {
	case OperationResolve:
		value, err := protocol.DecodeResolveVerification(vector.Verification)
		return err == nil && verifier.VerifyResolve(ctx, value) == nil
	case OperationRead:
		value, err := protocol.DecodeReadVerification(vector.Verification)
		return err == nil && verifier.VerifyRead(ctx, value) == nil
	default:
		return false
	}
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
