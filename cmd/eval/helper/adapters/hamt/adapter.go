// Package hamt configures the IPLD UnixFS HAMT baseline adapter.
package hamt

import (
	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/merkledag"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/merkledagimport"
)

// New creates an IPLD UnixFS + HAMT adapter.
func New(system *evalstore.System) *merkledag.Adapter {
	return merkledag.New(system, merkledag.Options{
		Name:      "hamt",
		DirLayout: merkledagimport.DirLayoutHAMT,
	})
}
