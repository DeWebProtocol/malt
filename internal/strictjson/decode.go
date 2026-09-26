package strictjson

import (
	"bytes"
	"encoding/json"
)

// Decode accepts one lossless JSON value with only the current schema's fields.
func Decode(data []byte, target any) error {
	if err := ValidateUnicode(data); err != nil {
		return err
	}
	if err := RejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
