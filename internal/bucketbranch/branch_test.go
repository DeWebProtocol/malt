package bucketbranch

import "testing"

func TestNormalizeSelectorMatchesGatewayBranchGrammar(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "", want: "main"},
		{raw: " main ", want: "main"},
		{raw: "photos", want: "photos"},
		{raw: "heads/team/photos", want: "team/photos"},
		{raw: "release/v1.2_candidate", want: "release/v1.2_candidate"},
	}
	for _, test := range tests {
		got, err := NormalizeSelector(test.raw)
		if err != nil || got != test.want {
			t.Errorf("NormalizeSelector(%q) = %q, %v; want %q", test.raw, got, err, test.want)
		}
	}
}

func TestNormalizeSelectorRejectsInvalidOrReservedBranches(t *testing.T) {
	for _, raw := range []string{
		"heads/main",
		"conflicts/alice/one",
		"/photos",
		"photos/",
		"team//photos",
		"team/../photos",
		"team\\photos",
		"team photos",
		"team:photos",
	} {
		if got, err := NormalizeSelector(raw); err == nil {
			t.Errorf("NormalizeSelector(%q) = %q, want error", raw, got)
		}
	}
}

func TestNormalizeExplicitRejectsDefaultBranch(t *testing.T) {
	for _, raw := range []string{"", "main", "heads/main"} {
		if got, err := NormalizeExplicit(raw); err == nil {
			t.Errorf("NormalizeExplicit(%q) = %q, want error", raw, got)
		}
	}
}
