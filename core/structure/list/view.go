package list

import cid "github.com/ipfs/go-cid"

// SliceView is an immutable in-memory list view used by list semantics and tests.
type SliceView struct {
	values []cid.Cid
}

// NewViewFromSlice creates an immutable list view from the supplied values.
func NewViewFromSlice(values []cid.Cid) *SliceView {
	cloned := make([]cid.Cid, len(values))
	copy(cloned, values)
	return &SliceView{values: cloned}
}

// Len returns the number of committed values.
func (v *SliceView) Len() uint64 {
	return uint64(len(v.values))
}

// Get returns the value at the given index.
func (v *SliceView) Get(index uint64) (cid.Cid, bool) {
	if index >= uint64(len(v.values)) {
		return cid.Undef, false
	}
	return v.values[index], true
}

// Iterate returns an iterator over the list values in index order.
func (v *SliceView) Iterate() Iterator {
	return &sliceIterator{values: v.values}
}

type sliceIterator struct {
	values []cid.Cid
	index  uint64
}

func (it *sliceIterator) Next() (uint64, cid.Cid, bool) {
	if it.index >= uint64(len(it.values)) {
		return 0, cid.Undef, false
	}
	index := it.index
	value := it.values[index]
	it.index++
	return index, value, true
}

func (it *sliceIterator) Err() error {
	return nil
}

var _ View = (*SliceView)(nil)
