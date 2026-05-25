package evalread

import (
	"bytes"
	"strings"
	"testing"
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

func TestCommandAcceptsReadbenchParsedBaselineSystems(t *testing.T) {
	var out bytes.Buffer
	cmd := newCommand("read", "test read", &out)
	cmd.SetArgs([]string{"--systems", "hamt, merkledag", "--iterations", "0"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output length = %d, want no output for zero iterations", out.Len())
	}
}

func TestCommandReturnsReadbenchParserErrorForInvalidSystems(t *testing.T) {
	var out bytes.Buffer
	cmd := newCommand("read", "test read", &out)
	cmd.SetArgs([]string{"--systems", "hamt,unknown", "--iterations", "0"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected invalid systems to fail")
	}
	if !strings.Contains(err.Error(), `unknown system "unknown"`) {
		t.Fatalf("error = %v, want readbench parser error", err)
	}
}
