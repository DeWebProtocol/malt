// Package lineage provides version lineage tracking for MALT structures.
package lineage

import (
	"context"
	"fmt"

	cid "github.com/ipfs/go-cid"
)

// ArcSetSnapshot provides the interface needed to count arcs for lineage recording.
type ArcSetSnapshot interface {
	Iterate() ArcIterator
}

// ArcIterator iterates over arcs in a snapshot.
type ArcIterator interface {
	Next() (path string, target cid.Cid, ok bool)
	Err() error
}

// RecorderAdapter adapts a lineage.Manager to the writer.LineageRecorder interface.
// It records lineage relationships and tracks arc counts via EAT snapshots.
type RecorderAdapter struct {
	manager *Manager
	snap    ArcSetSnapshot // optional, for counting arcs
}

// NewRecorderAdapter creates an adapter that bridges writer.LineageRecorder
// to lineage.Manager.
func NewRecorderAdapter(manager *Manager, snap ArcSetSnapshot) *RecorderAdapter {
	return &RecorderAdapter{
		manager: manager,
		snap:    snap,
	}
}

// Record records a lineage relationship: newRoot was derived from oldRoot.
// It counts arcs from the snapshot if available.
func (r *RecorderAdapter) Record(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid) error {
	arcCount := 0
	if r.snap != nil {
		iter := r.snap.Iterate()
		for {
			_, _, ok := iter.Next()
			if !ok {
				break
			}
			arcCount++
		}
		if iter.Err() != nil {
			return fmt.Errorf("arc count iteration failed: %w", iter.Err())
		}
	}

	return r.manager.Record(ctx, newRoot, oldRoot, arcCount)
}
