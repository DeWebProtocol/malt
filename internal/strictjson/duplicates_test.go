package strictjson

import (
	"strings"
	"testing"
)

func TestRejectDuplicateKeys(t *testing.T) {
	for _, test := range []struct {
		name    string
		data    string
		wantErr string
	}{
		{name: "unique nested", data: `{"outer":{"key":1},"items":[{"key":2}]}`},
		{name: "duplicate top level", data: `{"key":1,"key":2}`, wantErr: "duplicate JSON object key"},
		{name: "duplicate nested", data: `{"outer":{"key":1,"key":2}}`, wantErr: "duplicate JSON object key"},
		{name: "trailing value", data: `{"key":1} {"key":2}`, wantErr: "trailing JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := RejectDuplicateKeys([]byte(test.data))
			if test.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("error=%v, want %q", err, test.wantErr)
			}
		})
	}
}
