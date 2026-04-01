package hamt

import (
	"bytes"
	"fmt"

	ipld "github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/node/basicnode"
	cid "github.com/ipfs/go-cid"
)

// Node represents a parsed HAMT node.
// HAMT nodes are typically encoded as dag-cbor with the following structure:
//
//	{
//	  "0": <bitfield>,     // Big-int or bytes representing the bitmap
//	  "1": <entries>,      // Array of entries (values or links)
//	}
type Node struct {
	// Bitfield is the bitmap indicating which buckets are present.
	// Each bit corresponds to a potential bucket position.
	Bitfield []byte

	// Entries are the actual values or child links.
	Entries []Entry
}

// Entry represents an entry in the HAMT node.
// It can be either a value (leaf) or a link to a child node.
type Entry struct {
	// Value is the CID of the value (for leaf entries).
	Value cid.Cid

	// Link is the CID of the child node (for intermediate entries).
	Link cid.Cid

	// isValue indicates whether this is a value entry.
	isValue bool
}

// IsValue returns true if this entry is a value (leaf).
func (e *Entry) IsValue() bool {
	return e.isValue
}

// IsLink returns true if this entry is a link to a child node.
func (e *Entry) IsLink() bool {
	return !e.isValue && e.Link.Defined()
}

// ParseNode parses a HAMT node from dag-cbor encoded bytes.
// The expected structure is:
//
//	{
//	  "0": <bitfield bytes>,
//	  "1": <array of entries>
//	}
//
// Each entry in the array can be:
//   - A link (CID) to a value
//   - An array [link, value] for intermediate nodes
//   - A nested structure for complex values
func ParseNode(data []byte) (*Node, error) {
	// Parse as dag-cbor
	nb := basicnode.Prototype.Any.NewBuilder()
	if err := dagcbor.Decode(nb, bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("failed to decode dag-cbor: %w", err)
	}

	node := nb.Build()

	// Expect a map with "0" (bitfield) and "1" (entries)
	if node.Kind() != ipld.Kind_Map {
		return nil, fmt.Errorf("HAMT node must be a map, got %s", node.Kind())
	}

	// Get bitfield (key "0")
	bitfieldNode, err := node.LookupByString("0")
	if err != nil {
		return nil, fmt.Errorf("HAMT node missing bitfield (key '0'): %w", err)
	}

	bitfield, err := extractBytes(bitfieldNode)
	if err != nil {
		return nil, fmt.Errorf("failed to extract bitfield: %w", err)
	}

	// Get entries (key "1")
	entriesNode, err := node.LookupByString("1")
	if err != nil {
		return nil, fmt.Errorf("HAMT node missing entries (key '1'): %w", err)
	}

	if entriesNode.Kind() != ipld.Kind_List {
		return nil, fmt.Errorf("HAMT entries must be a list, got %s", entriesNode.Kind())
	}

	// Parse entries
	entries, err := parseEntries(entriesNode)
	if err != nil {
		return nil, fmt.Errorf("failed to parse entries: %w", err)
	}

	return &Node{
		Bitfield: bitfield,
		Entries:  entries,
	}, nil
}

// parseEntries extracts entries from the entries list.
func parseEntries(node ipld.Node) ([]Entry, error) {
	var entries []Entry

	iter := node.ListIterator()
	for !iter.Done() {
		_, entryNode, err := iter.Next()
		if err != nil {
			return nil, err
		}

		entry, err := parseEntry(entryNode)
		if err != nil {
			return nil, err
		}

		entries = append(entries, entry)
	}

	return entries, nil
}

// parseEntry parses a single entry from the HAMT entries list.
// Entry format can be:
//   - A link (CID) - points to a value
//   - An array [link, ...] - points to a child node with extra data
//   - Bytes - inline value
func parseEntry(node ipld.Node) (Entry, error) {
	switch node.Kind() {
	case ipld.Kind_Link:
		// Direct link to value
		link, err := node.AsLink()
		if err != nil {
			return Entry{}, err
		}
		c, err := linkToCID(link)
		if err != nil {
			return Entry{}, err
		}
		return Entry{Value: c, isValue: true}, nil

	case ipld.Kind_List:
		// Array format: [link, ...]
		// First element is the link, rest is metadata
		first, err := node.LookupByIndex(0)
		if err != nil {
			return Entry{}, fmt.Errorf("failed to get first element: %w", err)
		}

		if first.Kind() == ipld.Kind_Link {
			link, err := first.AsLink()
			if err != nil {
				return Entry{}, err
			}
			c, err := linkToCID(link)
			if err != nil {
				return Entry{}, err
			}
			// Check if it's a child link or value link
			// In IPFS HAMT, [link] is a child, [link, value] is a value
			length := node.Length()
			if length > 1 {
				// Has value - this is a value entry
				return Entry{Value: c, isValue: true}, nil
			}
			// Just a link - this is a child entry
			return Entry{Link: c, isValue: false}, nil
		}
		return Entry{}, fmt.Errorf("unexpected first element kind in array: %s", first.Kind())

	case ipld.Kind_Bytes:
		// Inline bytes value - convert to CID
		bytes, err := node.AsBytes()
		if err != nil {
			return Entry{}, err
		}
		// For inline values, we create a CID from the bytes
		// This is a simplified approach; real implementation might differ
		c, err := cid.Cast(bytes)
		if err != nil {
			// If it's not a valid CID, treat it as raw data
			// Create a raw CID
			return Entry{}, fmt.Errorf("inline bytes not supported yet")
		}
		return Entry{Value: c, isValue: true}, nil

	default:
		return Entry{}, fmt.Errorf("unexpected entry kind: %s", node.Kind())
	}
}

// extractBytes extracts bytes from a node (bytes or big-int encoded).
func extractBytes(node ipld.Node) ([]byte, error) {
	switch node.Kind() {
	case ipld.Kind_Bytes:
		return node.AsBytes()

	case ipld.Kind_Int:
		// Big-int encoding: convert to bytes
		i, err := node.AsInt()
		if err != nil {
			return nil, err
		}
		return bigIntToBytes(i), nil

	case ipld.Kind_Map:
		// Some implementations use a map with "/" for big-int
		bytes, _ := node.LookupByString("/")
		if bytes != nil {
			return extractBytes(bytes)
		}
		return nil, fmt.Errorf("unsupported map format for bitfield")

	default:
		return nil, fmt.Errorf("unsupported bitfield kind: %s", node.Kind())
	}
}

// bigIntToBytes converts a big integer to bytes.
func bigIntToBytes(n int64) []byte {
	if n == 0 {
		return []byte{0}
	}

	var bytes []byte
	for n > 0 {
		bytes = append([]byte{byte(n & 0xFF)}, bytes...)
		n >>= 8
	}

	return bytes
}

// isBitSet checks if a bit is set in the bitfield.
func isBitSet(bitfield []byte, pos int) bool {
	byteIdx := pos / 8
	bitIdx := pos % 8

	if byteIdx >= len(bitfield) {
		return false
	}

	return (bitfield[byteIdx] & (1 << (7 - bitIdx))) != 0
}

// countSetBitsBefore counts the number of set bits before a position.
// This gives the actual index in the entries array.
func countSetBitsBefore(bitfield []byte, pos int) int {
	count := 0

	// Count complete bytes before the target byte
	targetByte := pos / 8
	for i := 0; i < targetByte; i++ {
		count += popcount(bitfield[i])
	}

	// Count bits in the target byte before the target position
	bitIdx := pos % 8
	if targetByte < len(bitfield) {
		mask := byte(0xFF << (8 - bitIdx))
		count += popcount(bitfield[targetByte] & mask)
	}

	return count
}

// popcount counts the number of set bits in a byte.
func popcount(b byte) int {
	count := 0
	for b != 0 {
		count += int(b & 1)
		b >>= 1
	}
	return count
}

// linkToCID converts an ipld.Link to cid.Cid.
func linkToCID(link ipld.Link) (cid.Cid, error) {
	type cidLink interface {
		Cid() cid.Cid
	}
	if cl, ok := link.(cidLink); ok {
		return cl.Cid(), nil
	}
	return cid.Cid{}, fmt.Errorf("link is not a CID link: %T", link)
}