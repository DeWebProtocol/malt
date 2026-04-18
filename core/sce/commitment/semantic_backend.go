package commitment

import (
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ListBackend is the internal fixed-slot backend contract consumed by list semantics.
// Implementations authenticate stable index positions and may use caller-supplied
// values to rebuild transient proving state on cache miss.
type ListBackend interface {
	MaxValues() int
	CommitValues(values []cid.Cid) (cid.Cid, error)
	ProveIndex(root cid.Cid, values []cid.Cid, index uint64) (cid.Cid, []byte, error)
	VerifyIndex(root cid.Cid, index uint64, value cid.Cid, proof []byte) (bool, error)
	ReplaceIndex(root cid.Cid, values []cid.Cid, index uint64, oldValue, newValue cid.Cid) (cid.Cid, error)
}

// MappingBackend is the internal keyed backend contract consumed by map semantics.
type MappingBackend interface {
	CommitBindings(bindings arcset.ArcSet) (cid.Cid, error)
	ProveBinding(root cid.Cid, bindings arcset.ArcSet, key arcset.Path) (cid.Cid, bool, []byte, error)
	VerifyBinding(root cid.Cid, key arcset.Path, value cid.Cid, present bool, proof []byte) (bool, error)
	UpdateBinding(root cid.Cid, bindings arcset.ArcSet, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error)
}
