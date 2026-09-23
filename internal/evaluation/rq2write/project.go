// Package rq2write projects the source fixture's file operations directly into
// typed Core deltas. Native filesystem and browser source preparation remain
// separately measured callers of this shared application projection.
package rq2write

import (
	"bytes"
	"context"
	"fmt"
	"math"

	"github.com/dewebprotocol/malt-client/internal/evaluation/authenticationgraph"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

func Apply(ctx context.Context, edit *authenticationgraph.Edit, root cid.Cid, operation rq2fixture.Operation, payloads []cid.Cid) (cid.Cid, error) {
	state, err := edit.State(root)
	if err != nil {
		return cid.Undef, err
	}
	if state.Descriptor.Layout != maltcid.Prefix || state.Descriptor.DerivationProfile != uint8(derivation.SHA256) {
		return cid.Undef, fmt.Errorf("RQ2 file root requires Prefix AA1")
	}
	for _, payload := range payloads {
		if !payload.Defined() || payload.Type() != cid.Raw {
			return cid.Undef, fmt.Errorf("file operation requires raw payload CIDs")
		}
	}
	if operation.Kind == rq2fixture.KindListAppend || operation.Kind == rq2fixture.KindListReplace {
		edge, err := entryAt(state, operation.SourcePath)
		if err != nil {
			return cid.Undef, err
		}
		child, err := edit.State(edge.Target)
		if err != nil {
			return cid.Undef, err
		}
		if child.Descriptor.Layout != maltcid.Positional || child.Descriptor.DerivationProfile != uint8(derivation.Direct) || child.Descriptor.Profile != state.Descriptor.Profile || child.ChunkSize == 0 {
			return cid.Undef, fmt.Errorf("file requires a measured Positional Root with the parent's VC profile")
		}
		if len(payloads) != 1 {
			return cid.Undef, fmt.Errorf("chunk operation requires one payload")
		}
		var delta authentication.Delta
		if operation.Kind == rq2fixture.KindListAppend {
			if child.TotalSize%child.ChunkSize != 0 || operation.PayloadBytes != child.ChunkSize || child.TotalSize > math.MaxUint64-child.ChunkSize {
				return cid.Undef, fmt.Errorf("append requires a full final chunk and one same-width new chunk")
			}
			count := uint64(len(child.Entries)) + 1
			total := child.TotalSize + child.ChunkSize
			delta = authentication.Delta{Changes: []engine.Change{{Label: coordinate.EncodeIndex(count - 1), After: payloads[0]}}, Count: &count, TotalSize: &total}
		} else {
			if operation.ListIndex == nil || *operation.ListIndex >= uint64(len(child.Entries)) {
				return cid.Undef, fmt.Errorf("replacement index is outside measured file")
			}
			index := *operation.ListIndex
			size := min(child.ChunkSize, child.TotalSize-index*child.ChunkSize)
			if operation.PayloadBytes != size {
				return cid.Undef, fmt.Errorf("replacement must preserve the authenticated chunk length")
			}
			var before cid.Cid
			for _, entry := range child.Entries {
				if bytes.Equal(entry.Label, coordinate.EncodeIndex(index)) {
					before = entry.Target
					break
				}
			}
			if !before.Defined() {
				return cid.Undef, fmt.Errorf("replacement index is absent")
			}
			delta.Changes = []engine.Change{{Label: coordinate.EncodeIndex(index), Before: before, After: payloads[0]}}
		}
		nextChild, err := edit.Apply(ctx, edge.Target, delta)
		if err != nil {
			return cid.Undef, err
		}
		// Select the caller's exact file edge; never rewrite every parent of an
		// aliased immutable child or reject a valid copy-on-write graph.
		return edit.Apply(ctx, root, authentication.Delta{Changes: []engine.Change{{Label: edge.Label, Before: edge.Target, After: nextChild}}})
	}
	insert := func(path string, payload cid.Cid) (engine.Change, error) {
		if path == "" {
			return engine.Change{}, fmt.Errorf("insert path is empty")
		}
		for _, entry := range state.Entries {
			if bytes.Equal(entry.Label, []byte(path)) {
				return engine.Change{}, fmt.Errorf("destination %q already exists", path)
			}
		}
		return engine.Change{Label: []byte(path), After: payload}, nil
	}
	var changes []engine.Change
	switch operation.Kind {
	case rq2fixture.KindBatchInsert:
		if len(payloads) != len(operation.Batch) || len(payloads) == 0 {
			return cid.Undef, fmt.Errorf("batch payload count differs from declared destinations")
		}
		for i, payload := range payloads {
			change, err := insert(operation.Batch[i].Path, payload)
			if err != nil {
				return cid.Undef, err
			}
			changes = append(changes, change)
		}
	case rq2fixture.KindDirectInsert:
		if len(payloads) != 1 {
			return cid.Undef, fmt.Errorf("insert requires one payload")
		}
		change, err := insert(operation.DestinationPath, payloads[0])
		if err != nil {
			return cid.Undef, err
		}
		changes = []engine.Change{change}
	case rq2fixture.KindDirectReplace, rq2fixture.KindDocumentEdit, rq2fixture.KindDirectDelete, rq2fixture.KindDirectMove:
		entry, err := entryAt(state, operation.SourcePath)
		if err != nil {
			return cid.Undef, err
		}
		if entry.Target.Type() != cid.Raw {
			return cid.Undef, fmt.Errorf("source %q is not a direct file", operation.SourcePath)
		}
		change := engine.Change{Label: entry.Label, Before: entry.Target}
		if operation.Kind == rq2fixture.KindDirectReplace || operation.Kind == rq2fixture.KindDocumentEdit {
			if len(payloads) != 1 {
				return cid.Undef, fmt.Errorf("replacement requires one payload")
			}
			change.After = payloads[0]
		} else if len(payloads) != 0 {
			return cid.Undef, fmt.Errorf("metadata operation cannot carry payloads")
		}
		changes = []engine.Change{change}
		if operation.Kind == rq2fixture.KindDirectMove {
			destination, err := insert(operation.DestinationPath, entry.Target)
			if err != nil {
				return cid.Undef, err
			}
			changes = append(changes, destination)
		}
	default:
		return cid.Undef, fmt.Errorf("unsupported source operation %q", operation.Kind)
	}
	return edit.Apply(ctx, root, authentication.Delta{Changes: changes})
}
func entryAt(state engine.State, path string) (engine.Entry, error) {
	for _, entry := range state.Entries {
		if bytes.Equal(entry.Label, []byte(path)) {
			return entry, nil
		}
	}
	return engine.Entry{}, fmt.Errorf("source %q is absent", path)
}
