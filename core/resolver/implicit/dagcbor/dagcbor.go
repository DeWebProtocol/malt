// Package dagcbor implements the dag-cbor IPLD codec.
package dagcbor

import (
	"bytes"
	"fmt"

	ipld "github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagcbor"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

// Codec implements the dag-cbor codec.
type Codec struct{}

// New creates a new dag-cbor codec.
func New() *Codec {
	return &Codec{}
}

// Name returns "dag-cbor".
func (c *Codec) Name() string {
	return "dag-cbor"
}

// Decode parses dag-cbor bytes into an IPLD Node.
func (c *Codec) Decode(data []byte) (ipld.Node, error) {
	nb := basicnode.Prototype.Any.NewBuilder()
	if err := dagcbor.Decode(nb, bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("dag-cbor decode failed: %w", err)
	}
	return nb.Build(), nil
}

// Encode encodes an IPLD Node to dag-cbor bytes.
func (c *Codec) Encode(node ipld.Node) ([]byte, error) {
	var buf bytes.Buffer
	if err := dagcbor.Encode(node, &buf); err != nil {
		return nil, fmt.Errorf("dag-cbor encode failed: %w", err)
	}
	return buf.Bytes(), nil
}