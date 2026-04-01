// Package dagjson implements the dag-json IPLD codec.
package dagjson

import (
	"bytes"
	"fmt"

	ipld "github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/codec/dagjson"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

// Codec implements the dag-json codec.
type Codec struct{}

// New creates a new dag-json codec.
func New() *Codec {
	return &Codec{}
}

// Name returns "dag-json".
func (c *Codec) Name() string {
	return "dag-json"
}

// Decode parses dag-json bytes into an IPLD Node.
func (c *Codec) Decode(data []byte) (ipld.Node, error) {
	nb := basicnode.Prototype.Any.NewBuilder()
	if err := dagjson.Decode(nb, bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("dag-json decode failed: %w", err)
	}
	return nb.Build(), nil
}

// Encode encodes an IPLD Node to dag-json bytes.
func (c *Codec) Encode(node ipld.Node) ([]byte, error) {
	var buf bytes.Buffer
	if err := dagjson.Encode(node, &buf); err != nil {
		return nil, fmt.Errorf("dag-json encode failed: %w", err)
	}
	return buf.Bytes(), nil
}