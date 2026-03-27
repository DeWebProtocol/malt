package types

import (
	"testing"
)

func TestNewCID(t *testing.T) {
	data := []byte("test data")
	cid, err := NewCID(data)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	if cid.IsEmpty() {
		t.Error("CID should not be empty")
	}

	// Same data should produce same CID
	cid2, err := NewCID(data)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	if !cid.Equals(cid2) {
		t.Error("Same data should produce same CID")
	}

	// Different data should produce different CID
	cid3, err := NewCID([]byte("different data"))
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	if cid.Equals(cid3) {
		t.Error("Different data should produce different CID")
	}
}

func TestParseCID(t *testing.T) {
	data := []byte("test data")
	cid, err := NewCID(data)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	// Parse the CID string
	parsed, err := ParseCID(cid.String())
	if err != nil {
		t.Fatalf("ParseCID failed: %v", err)
	}

	if !cid.Equals(parsed) {
		t.Error("Parsed CID should equal original")
	}
}

func TestCIDJSON(t *testing.T) {
	data := []byte("test data")
	cid, err := NewCID(data)
	if err != nil {
		t.Fatalf("NewCID failed: %v", err)
	}

	// Marshal to JSON
	jsonData, err := cid.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	// Unmarshal from JSON
	var parsed CID
	if err := parsed.UnmarshalJSON(jsonData); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if !cid.Equals(parsed) {
		t.Error("Unmarshalled CID should equal original")
	}
}

func TestPath(t *testing.T) {
	p := Path("links/0")

	if p.String() != "links/0" {
		t.Errorf("Path.String() = %s, want links/0", p.String())
	}

	if !p.IsValid() {
		t.Error("Path should be valid")
	}

	emptyPath := Path("")
	if emptyPath.IsValid() {
		t.Error("Empty path should be invalid")
	}
}

func TestArcPair(t *testing.T) {
	cid, _ := NewCID([]byte("target"))
	pair := NewArcPair("link", cid)

	if pair.Path != "link" {
		t.Errorf("ArcPair.Path = %s, want link", pair.Path)
	}

	if !pair.Target.Equals(cid) {
		t.Error("ArcPair.Target should equal cid")
	}
}