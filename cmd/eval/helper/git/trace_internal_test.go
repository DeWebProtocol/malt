package git

import (
	"slices"
	"testing"
)

func TestRevListArgsUseTopoOrder(t *testing.T) {
	args := revListArgs("HEAD", true)
	if !slices.Contains(args, "--topo-order") {
		t.Fatalf("rev-list args = %v, want --topo-order", args)
	}
	if !slices.Contains(args, "--first-parent") {
		t.Fatalf("rev-list args = %v, want --first-parent", args)
	}
}
