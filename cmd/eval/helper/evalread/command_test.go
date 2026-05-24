package evalread

import (
	"reflect"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
)

func TestParseArcFlagsRequiresPathAndCID(t *testing.T) {
	if _, err := ParseArcFlags([]string{"missing-separator"}); err == nil {
		t.Fatal("expected missing separator to fail")
	}
	if _, err := ParseArcFlags([]string{"=bafy"}); err == nil {
		t.Fatal("expected empty path to fail")
	}
	if _, err := ParseArcFlags([]string{"@payload="}); err == nil {
		t.Fatal("expected empty cid to fail")
	}
}

func TestParseArcFlagsReturnsMap(t *testing.T) {
	got, err := ParseArcFlags([]string{"@payload=bafyroot", "name=bafyname"})
	if err != nil {
		t.Fatalf("parse arcs: %v", err)
	}
	if got["@payload"] != "bafyroot" || got["name"] != "bafyname" {
		t.Fatalf("arcs = %#v", got)
	}
}

func TestParseSystemsCSVReturnsOrderedSystems(t *testing.T) {
	got, err := ParseSystemsCSV("maltflat, merkledag, hamt")
	if err != nil {
		t.Fatalf("parse systems: %v", err)
	}
	want := []readbench.SystemName{
		readbench.SystemMALTFlat,
		readbench.SystemMerkleDAG,
		readbench.SystemHAMT,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("systems = %q, want %q", got, want)
	}
}

func TestParseSystemsCSVRejectsUnknownSystem(t *testing.T) {
	if _, err := ParseSystemsCSV("maltflat,unknown"); err == nil {
		t.Fatal("expected unknown system to fail")
	}
}

func TestParseSystemsCSVRejectsDuplicateSystem(t *testing.T) {
	if _, err := ParseSystemsCSV("maltflat,merkledag,maltflat"); err == nil {
		t.Fatal("expected duplicate system to fail")
	}
}
