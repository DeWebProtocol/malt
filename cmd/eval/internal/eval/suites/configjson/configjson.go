// Package configjson decodes evaluation suite config JSON consistently.
package configjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// Decode unmarshals one suite config object while rejecting unknown fields.
func Decode(raw json.RawMessage, suite string, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("parse %s config: %w", suite, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("parse %s config: unexpected trailing JSON", suite)
		}
		return fmt.Errorf("parse %s config: %w", suite, err)
	}
	return nil
}
