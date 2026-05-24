// Package codec defines the IPLD codec interface for parsing blocks and extracting links.
// It wraps go-ipld-prime for codec implementations.
package codec

import (
	"fmt"

	"github.com/ipfs/go-cid"
	ipld "github.com/ipld/go-ipld-prime"
)

// Codec parses raw bytes into an IPLD Node using a specific codec.
type Codec interface {
	// Name returns the codec name (e.g., "dag-cbor", "dag-json").
	Name() string

	// Decode parses raw bytes into an IPLD Node.
	Decode(data []byte) (ipld.Node, error)

	// Encode encodes an IPLD Node back to bytes.
	Encode(node ipld.Node) ([]byte, error)
}

// Registry holds registered codecs.
type Registry struct {
	codecs map[string]Codec
}

// NewRegistry creates a new codec registry.
func NewRegistry() *Registry {
	return &Registry{
		codecs: make(map[string]Codec),
	}
}

// Register registers a codec.
func (r *Registry) Register(codec Codec) {
	r.codecs[codec.Name()] = codec
}

// Get returns a codec by name.
func (r *Registry) Get(name string) (Codec, error) {
	codec, ok := r.codecs[name]
	if !ok {
		return nil, fmt.Errorf("unknown codec: %s", name)
	}
	return codec, nil
}

// LinkToCID converts an ipld.Link to cid.Cid.
func LinkToCID(link ipld.Link) (cid.Cid, error) {
	// The default Link implementation in go-ipld-prime is cidlink.Link
	// which wraps a cid.Cid
	type cidLink interface {
		Cid() cid.Cid
	}
	if cl, ok := link.(cidLink); ok {
		return cl.Cid(), nil
	}
	return cid.Cid{}, fmt.Errorf("link is not a CID link: %T", link)
}

// ExtractLinks extracts all direct links from a node.
// For map/list nodes, it finds all link values (not nested).
// Returns a map from path key to CID.
func ExtractLinks(node ipld.Node) (map[string]cid.Cid, error) {
	links := make(map[string]cid.Cid)

	switch node.Kind() {
	case ipld.Kind_Map:
		iter := node.MapIterator()
		for !iter.Done() {
			key, value, err := iter.Next()
			if err != nil {
				break
			}
			if value.Kind() == ipld.Kind_Link {
				linkNode, err := value.AsLink()
				if err != nil {
					continue
				}
				c, err := LinkToCID(linkNode)
				if err != nil {
					continue
				}
				keyStr, err := key.AsString()
				if err != nil {
					continue
				}
				links[keyStr] = c
			}
		}

	case ipld.Kind_List:
		iter := node.ListIterator()
		for !iter.Done() {
			idx, value, err := iter.Next()
			if err != nil {
				break
			}
			if value.Kind() == ipld.Kind_Link {
				linkNode, err := value.AsLink()
				if err != nil {
					continue
				}
				c, err := LinkToCID(linkNode)
				if err != nil {
					continue
				}
				links[fmt.Sprintf("%d", idx)] = c
			}
		}

	case ipld.Kind_Link:
		linkNode, err := node.AsLink()
		if err != nil {
			return nil, err
		}
		c, err := LinkToCID(linkNode)
		if err != nil {
			return nil, err
		}
		links[""] = c
	}

	return links, nil
}

// ResolveLink resolves a path through an IPLD node.
// Returns the target CID and remaining path.
func ResolveLink(node ipld.Node, path []string) (cid.Cid, []string, error) {
	current := node
	remaining := path

	for len(remaining) > 0 {
		segment := remaining[0]

		switch current.Kind() {
		case ipld.Kind_Map:
			next, err := current.LookupByString(segment)
			if err != nil {
				return cid.Cid{}, remaining, fmt.Errorf("path segment not found: %s", segment)
			}

			// If it's a link, return the CID
			if next.Kind() == ipld.Kind_Link {
				linkNode, err := next.AsLink()
				if err != nil {
					return cid.Cid{}, remaining, err
				}
				c, err := LinkToCID(linkNode)
				if err != nil {
					return cid.Cid{}, remaining, err
				}
				return c, remaining[1:], nil
			}

			// Continue traversing
			current = next
			remaining = remaining[1:]

		case ipld.Kind_List:
			// Try to parse segment as index
			var idx int
			_, err := fmt.Sscanf(segment, "%d", &idx)
			if err != nil {
				return cid.Cid{}, remaining, fmt.Errorf("list index expected, got: %s", segment)
			}
			next, err := current.LookupByIndex(int64(idx))
			if err != nil {
				return cid.Cid{}, remaining, fmt.Errorf("list index out of range: %d", idx)
			}

			if next.Kind() == ipld.Kind_Link {
				linkNode, err := next.AsLink()
				if err != nil {
					return cid.Cid{}, remaining, err
				}
				c, err := LinkToCID(linkNode)
				if err != nil {
					return cid.Cid{}, remaining, err
				}
				return c, remaining[1:], nil
			}

			current = next
			remaining = remaining[1:]

		default:
			// Can't traverse further
			return cid.Cid{}, remaining, fmt.Errorf("cannot traverse node of kind: %s", current.Kind())
		}
	}

	// If we've consumed all path segments, check if current is a link
	if current.Kind() == ipld.Kind_Link {
		linkNode, err := current.AsLink()
		if err != nil {
			return cid.Cid{}, nil, err
		}
		c, err := LinkToCID(linkNode)
		if err != nil {
			return cid.Cid{}, nil, err
		}
		return c, nil, nil
	}

	return cid.Cid{}, nil, fmt.Errorf("path consumed but no link found")
}
