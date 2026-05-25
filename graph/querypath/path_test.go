package querypath_test

import (
	"testing"

	"github.com/dewebprotocol/malt/graph/querypath"
)

func TestCanonicalizeQueryPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", ""},
		{"/", ""},
		{"  /  ", ""},
		{"docs", "docs"},
		{"/docs/readme.md", "docs/readme.md"},
	}
	for _, tc := range tests {
		if got := querypath.CanonicalizeQueryPath(tc.in); got != tc.want {
			t.Errorf("CanonicalizeQueryPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
