package commitment

import (
	cid "github.com/ipfs/go-cid"
)

// ListBackend is the primitive fixed-slot contract consumed by higher semantic
// layers. Implementations authenticate stable index positions only; layout,
// length, and other metadata remain semantic-layer responsibilities. Caller-
// supplied values may be used to rebuild transient proving state on cache miss.
type ListBackend interface {
	MaxValues() int
	CommitValues(values []cid.Cid) (cid.Cid, error)
	ProveIndex(root cid.Cid, values []cid.Cid, index uint64) (cid.Cid, []byte, error)
	VerifyIndex(root cid.Cid, index uint64, value cid.Cid, proof []byte) (bool, error)
	ReplaceIndex(root cid.Cid, values []cid.Cid, index uint64, oldValue, newValue cid.Cid) (cid.Cid, error)
}
