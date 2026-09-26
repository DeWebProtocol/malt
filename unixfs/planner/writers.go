package planner

import (
	"container/list"
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

const writerCacheBytes = 64 << 20
const writerCacheRoots = 64

type cachedWriter struct {
	root   string
	writer *authentication.Writer
	bytes  uint64
}
type writerCache struct {
	roots map[string]*list.Element
	lru   list.List
	bytes uint64
}

// loadWriter imports each untrusted before-image once while resident. Entries
// are immutable, exact-Root qualified and scoped to this planner's source and
// engine. Only source-loaded states enter this cache, never unpublished plans.
func (p *Planner) loadWriter(ctx context.Context, root cid.Cid) (*authentication.Writer, error) {
	c := &p.writers
	if item := c.roots[root.KeyString()]; item != nil {
		c.lru.MoveToFront(item)
		return item.Value.(cachedWriter).writer, nil
	}
	candidate, err := p.remote.AuthenticationCandidate(ctx, root)
	if err != nil {
		return nil, err
	}
	if candidate == nil || candidate.Root != root.String() {
		return nil, fmt.Errorf("directory source substituted selected Root")
	}
	if len(candidate.State.Entries) > 65536 {
		return nil, fmt.Errorf("directory entry bound exceeded")
	}
	writer, err := authentication.NewWriter(ctx, p.engine, *candidate)
	if err != nil {
		return nil, err
	}
	// Conservatively charge vectors plus retained binding/tree bookkeeping.
	size := uint64(1024 + len(root.Bytes()))
	for _, entry := range candidate.State.Entries {
		size += uint64(512 + len(entry.Label) + len(entry.Target.Bytes()))
	}
	for _, node := range candidate.Nodes {
		size += uint64(512 + len(node.Reference) + 48*len(node.Cells))
		for _, cell := range node.Cells {
			size += uint64(len(cell))
		}
	}
	if size <= writerCacheBytes {
		if c.roots == nil {
			c.roots = map[string]*list.Element{}
		}
		for c.bytes+size > writerCacheBytes || c.lru.Len() >= writerCacheRoots {
			item := c.lru.Back()
			old := item.Value.(cachedWriter)
			delete(c.roots, old.root)
			c.bytes -= old.bytes
			c.lru.Remove(item)
		}
		c.roots[root.KeyString()] = c.lru.PushFront(cachedWriter{root.KeyString(), writer, size})
		c.bytes += size
	}
	return writer, nil
}
