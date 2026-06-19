package querypath_test

import (
	"errors"
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
		got, err := querypath.CanonicalizeQueryPath(tc.in)
		if err != nil {
			t.Fatalf("CanonicalizeQueryPath(%q) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("CanonicalizeQueryPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalizeQueryPathRejectsBadInputs(t *testing.T) {
	tests := []string{
		".",
		"..",
		"docs/./readme.md",
		"docs/../readme.md",
		"docs//readme.md",
		"docs/readme.md/",
		"docs/\x00/readme.md",
	}
	for _, input := range tests {
		if got, err := querypath.CanonicalizeQueryPath(input); err == nil {
			t.Errorf("CanonicalizeQueryPath(%q) = %q, want error", input, got)
		} else if !errors.Is(err, querypath.ErrInvalidQueryPath) {
			t.Errorf("CanonicalizeQueryPath(%q) error = %v, want ErrInvalidQueryPath", input, err)
		}
	}
}
