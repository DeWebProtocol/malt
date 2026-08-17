package strictjson

import "testing"

func TestValidateUnicode(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
	}{
		{name: "plain", data: []byte(`{"identity":"bucket-one"}`)},
		{name: "valid UTF-8", data: []byte(`{"identity":"数据"}`)},
		{name: "surrogate pair", data: []byte(`{"identity":"\ud83d\ude80"}`)},
		{name: "escaped slash then text", data: []byte(`{"identity":"\\ud800"}`)},
		{name: "invalid UTF-8", data: []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, wantErr: true},
		{name: "lone high surrogate", data: []byte(`{"identity":"\ud800"}`), wantErr: true},
		{name: "high then non-low surrogate", data: []byte(`{"identity":"\ud800\u0041"}`), wantErr: true},
		{name: "lone low surrogate", data: []byte(`{"identity":"\udc00"}`), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateUnicode(test.data)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateUnicode() error = %v, want error %v", err, test.wantErr)
			}
		})
	}
}
