package arcset

import (
	"bytes"
	"errors"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestCanonicalArcDeltaBinaryRoundTrip(t *testing.T) {
	before := NewCASTarget(deltaTestCID(t, "before"))
	after := NewMapTarget(deltaTestCID(t, "after"))
	insert := NewListTarget(deltaTestCID(t, "insert"))
	delta, err := NewCanonicalArcDelta(KindMap, []ArcChange{
		{Coordinate: mustMapCoordinate(t, "z"), Before: &before, After: &after},
		{Coordinate: mustMapCoordinate(t, "a"), After: &insert},
	})
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := delta.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := UnmarshalCanonicalArcDelta(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := decoded.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, reencoded) {
		t.Fatalf("delta encoding changed after round trip\nfirst:  %x\nsecond: %x", encoded, reencoded)
	}
	changes := decoded.Changes()
	if len(changes) != 2 || changes[0].Coordinate.String() != "a" || changes[0].Before != nil || changes[0].After == nil {
		t.Fatalf("decoded changes = %#v", changes)
	}
}

func TestCanonicalArcDeltaBinaryRejectsTampering(t *testing.T) {
	target := NewCASTarget(deltaTestCID(t, "target"))
	delta, err := NewCanonicalArcDelta(KindMap, []ArcChange{{
		Coordinate: mustMapCoordinate(t, "name"),
		After:      &target,
	}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := delta.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "truncated", data: encoded[:len(encoded)-1]},
		{name: "trailing", data: append(append([]byte(nil), encoded...), 0)},
		{name: "wrong magic", data: append([]byte("XXXX"), encoded[4:]...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := UnmarshalCanonicalArcDelta(test.data); err == nil {
				t.Fatal("tampered delta encoding was accepted")
			}
		})
	}

	badMarker := append([]byte(nil), encoded...)
	// MARC/MDLT header + version + kind length/kind + count + coordinate
	// length/coordinate places the first optional target marker here.
	marker := bytes.Index(badMarker, []byte("name")) + len("name")
	badMarker[marker] = 2
	if _, err := UnmarshalCanonicalArcDelta(badMarker); err == nil || errors.Is(err, nil) {
		t.Fatal("invalid optional target marker was accepted")
	}
}

func mustMapCoordinate(t *testing.T, value string) CanonicalCoordinate {
	t.Helper()
	coordinate, err := NewMapCoordinate(value)
	if err != nil {
		t.Fatal(err)
	}
	return coordinate
}

func deltaTestCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(value), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}
