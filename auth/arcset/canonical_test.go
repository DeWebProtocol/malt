package arcset

import (
	"bytes"
	"errors"
	"testing"

	cid "github.com/ipfs/go-cid"
)

func TestCanonicalMapOrderingAndDeterministicEncoding(t *testing.T) {
	payload := testCID(t, "payload")
	a := testCID(t, "a")
	b := testCID(t, "b")

	first, err := NewCanonicalArcSet(KindMap, []ArcEntry{
		mustMapEntry(t, "b", NewCASTarget(b)),
		mustMapEntry(t, "@payload", NewCASTarget(payload)),
		mustMapEntry(t, "a", NewCASTarget(a)),
	})
	if err != nil {
		t.Fatalf("NewCanonicalArcSet(first) failed: %v", err)
	}

	second, err := NewCanonicalArcSet(KindMap, []ArcEntry{
		mustMapEntry(t, "a", NewCASTarget(a)),
		mustMapEntry(t, "b", NewCASTarget(b)),
		mustMapEntry(t, "@payload", NewCASTarget(payload)),
	})
	if err != nil {
		t.Fatalf("NewCanonicalArcSet(second) failed: %v", err)
	}

	got := coordinateStrings(first.Entries())
	want := []string{"@payload", "a", "b"}
	if !equalStrings(got, want) {
		t.Fatalf("entry order = %v, want %v", got, want)
	}

	firstBytes, err := first.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary(first) failed: %v", err)
	}
	secondBytes, err := second.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary(second) failed: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("differently ordered canonical map inputs encoded differently")
	}
}

func TestCanonicalArcSetDuplicateRejectionAndCollapse(t *testing.T) {
	target := testCID(t, "same")
	coord := mustListCoordinate(t, 0)

	collapsed, err := NewCanonicalArcSet(KindList, []ArcEntry{
		{Coordinate: coord, Target: NewCASTarget(target)},
		{Coordinate: coord, Target: NewCASTarget(target)},
	})
	if err != nil {
		t.Fatalf("equivalent duplicate rejected: %v", err)
	}
	if got := len(collapsed.Entries()); got != 1 {
		t.Fatalf("collapsed length = %d, want 1", got)
	}

	_, err = NewCanonicalArcSet(KindList, []ArcEntry{
		{Coordinate: coord, Target: NewCASTarget(target)},
		{Coordinate: coord, Target: NewCASTarget(testCID(t, "different"))},
	})
	if !errors.Is(err, ErrDuplicateCoordinate) {
		t.Fatalf("err = %v, want ErrDuplicateCoordinate", err)
	}
}

func TestCanonicalMapDoesNotRequirePayload(t *testing.T) {
	target := testCID(t, "a")
	set, err := NewCanonicalMapArcSet(map[string]cid.Cid{"a": target})
	if err != nil {
		t.Fatalf("NewCanonicalMapArcSet failed: %v", err)
	}
	entries := set.Entries()
	if len(entries) != 1 || entries[0].Coordinate.String() != "a" || !entries[0].Target.CID().Equals(target) {
		t.Fatalf("entries = %#v, want one a binding", entries)
	}
}

func TestCanonicalListUsesNumericIndexesWithoutPayload(t *testing.T) {
	second := testCID(t, "second")
	first := testCID(t, "first")

	set, err := NewCanonicalListArcSetFromIndexed(map[uint64]cid.Cid{
		1: second,
		0: first,
	})
	if err != nil {
		t.Fatalf("NewCanonicalListArcSetFromIndexed failed: %v", err)
	}

	entries := set.Entries()
	got := coordinateStrings(entries)
	want := []string{"0", "1"}
	if !equalStrings(got, want) {
		t.Fatalf("entry order = %v, want %v", got, want)
	}
	if !entries[0].Target.CID().Equals(first) || !entries[1].Target.CID().Equals(second) {
		t.Fatal("list entries not sorted by numeric index")
	}
}

func TestCanonicalArcSetRoundTripSerialization(t *testing.T) {
	payload := testCID(t, "payload")
	child := testCID(t, "child-map")

	original, err := NewCanonicalArcSet(KindMap, []ArcEntry{
		mustMapEntry(t, "@payload", NewCASTarget(payload)),
		mustMapEntry(t, "child", NewMapTarget(child)),
	})
	if err != nil {
		t.Fatalf("NewCanonicalArcSet failed: %v", err)
	}

	encoded, err := original.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary failed: %v", err)
	}
	decoded, err := UnmarshalCanonicalArcSet(encoded)
	if err != nil {
		t.Fatalf("UnmarshalCanonicalArcSet failed: %v", err)
	}

	reencoded, err := decoded.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary(decoded) failed: %v", err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatal("round-trip encoding changed bytes")
	}
	if decoded.Kind() != KindMap {
		t.Fatalf("kind = %s, want %s", decoded.Kind(), KindMap)
	}
	entries := decoded.Entries()
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
	if entries[1].Target.Kind() != TargetKindMap || !entries[1].Target.CID().Equals(child) {
		t.Fatalf("decoded child target = (%s, %s), want map %s", entries[1].Target.Kind(), entries[1].Target.CID(), child)
	}
}

func mustMapEntry(t *testing.T, raw string, target TargetRef) ArcEntry {
	t.Helper()
	coord, err := NewMapCoordinate(raw)
	if err != nil {
		t.Fatalf("NewMapCoordinate(%q) failed: %v", raw, err)
	}
	return ArcEntry{Coordinate: coord, Target: target}
}

func mustListCoordinate(t *testing.T, index int64) CanonicalCoordinate {
	t.Helper()
	coord, err := NewListCoordinate(index)
	if err != nil {
		t.Fatalf("NewListCoordinate(%d) failed: %v", index, err)
	}
	return coord
}

func coordinateStrings(entries []ArcEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Coordinate.String()
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
