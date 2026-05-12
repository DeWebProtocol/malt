package evalread

import "testing"

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
