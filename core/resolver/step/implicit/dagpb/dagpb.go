// Package dagpb implements the dag-pb (Protocol Buffers) IPLD codec.
// dag-pb is used by UnixFS and other protocols in IPFS.
package dagpb

import (
	"bytes"
	"fmt"

	"github.com/ipld/go-codec-dagpb"
	ipld "github.com/ipld/go-ipld-prime"
	"github.com/ipld/go-ipld-prime/node/basicnode"
)

// Codec implements the dag-pb codec.
type Codec struct{}

// New creates a new dag-pb codec.
func New() *Codec {
	return &Codec{}
}

// Name returns "dag-pb".
func (c *Codec) Name() string {
	return "dag-pb"
}

// Decode parses dag-pb bytes into an IPLD Node.
func (c *Codec) Decode(data []byte) (ipld.Node, error) {
	nb := basicnode.Prototype.Any.NewBuilder()
	if err := dagpb.Decode(nb, bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("dag-pb decode failed: %w", err)
	}
	return nb.Build(), nil
}

// Encode encodes an IPLD Node to dag-pb bytes.
func (c *Codec) Encode(node ipld.Node) ([]byte, error) {
	var buf bytes.Buffer
	if err := dagpb.Encode(node, &buf); err != nil {
		return nil, fmt.Errorf("dag-pb encode failed: %w", err)
	}
	return buf.Bytes(), nil
}
