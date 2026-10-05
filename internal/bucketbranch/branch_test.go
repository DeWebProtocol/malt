package bucketbranch

import "testing"

func TestNormalizeSelectorMatchesGatewayBranchGrammar(t *testing.T) {
	tests := []struct {
		raw  string
		want string
		ref  string
	}{
		{raw: "", want: "main", ref: "main"},
		{raw: " main ", want: "main", ref: "main"},
		{raw: "photos", want: "photos", ref: "heads/photos"},
		{raw: "heads/team/photos", want: "team/photos", ref: "heads/team/photos"},
		{raw: "release/v1.2_candidate", want: "release/v1.2_candidate", ref: "heads/release/v1.2_candidate"},
		{raw: "heads", want: "heads", ref: "heads/heads"},
		{raw: " heads/heads/topic ", want: "heads/heads/topic", ref: "heads/heads/topic"},
		{raw: "heads/heads/heads/topic", want: "heads/heads/heads/topic", ref: "heads/heads/heads/topic"},
		{raw: "heads/heads/main", want: "heads/heads/main", ref: "heads/heads/main"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			selector := test.raw
			for i := 0; i < 3; i++ {
				got, err := NormalizeSelector(selector)
				if err != nil || got != test.want {
					t.Fatalf("NormalizeSelector(%q) = %q, %v; want %q", selector, got, err, test.want)
				}
				if ref, err := RefName(selector); err != nil || ref != test.ref {
					t.Fatalf("RefName(%q) = %q, %v; want %q", selector, ref, err, test.ref)
				}
				selector = got
			}
		})
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
		if got, err := RefName(raw); err == nil {
			t.Errorf("RefName(%q) = %q, want error", raw, got)
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
