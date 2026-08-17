// Package strictjson rejects lossy Unicode before data reaches encoding/json.
package strictjson

import (
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// ValidateUnicode rejects invalid UTF-8 and unpaired UTF-16 surrogate escapes.
// encoding/json otherwise replaces both forms with U+FFFD during Unmarshal,
// which is unsafe for persisted identities used as map or idempotency keys.
func ValidateUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON is not valid UTF-8")
	}
	for offset := 0; offset < len(data); offset++ {
		if data[offset] != '"' {
			continue
		}
		offset++
		for ; offset < len(data) && data[offset] != '"'; offset++ {
			if data[offset] != '\\' {
				continue
			}
			offset++
			if offset >= len(data) || data[offset] != 'u' {
				continue
			}
			value, ok := decodeEscape(data, offset+1)
			if !ok {
				continue // encoding/json reports malformed escape syntax.
			}
			offset += 4
			if value >= 0xdc00 && value <= 0xdfff {
				return fmt.Errorf("JSON contains an unpaired low surrogate escape")
			}
			if value < 0xd800 || value > 0xdbff {
				continue
			}
			if offset+6 >= len(data) || data[offset+1] != '\\' || data[offset+2] != 'u' {
				return fmt.Errorf("JSON contains an unpaired high surrogate escape")
			}
			low, ok := decodeEscape(data, offset+3)
			if !ok || !utf16.IsSurrogate(rune(low)) || low < 0xdc00 {
				return fmt.Errorf("JSON contains an unpaired high surrogate escape")
			}
			offset += 6
		}
	}
	return nil
}

func decodeEscape(data []byte, start int) (uint16, bool) {
	if start+4 > len(data) {
		return 0, false
	}
	var value uint16
	for _, character := range data[start : start+4] {
		value <<= 4
		switch {
		case character >= '0' && character <= '9':
			value |= uint16(character - '0')
		case character >= 'a' && character <= 'f':
			value |= uint16(character-'a') + 10
		case character >= 'A' && character <= 'F':
			value |= uint16(character-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
