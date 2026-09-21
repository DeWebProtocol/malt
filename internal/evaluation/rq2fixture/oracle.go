package rq2fixture

import (
	"fmt"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/authenticationgraph"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// ValidateGraphAgainstSource is an independent full post-image oracle. It
// checks every typed binding, measured sequence descriptor, chunk CID and
// reachable object against source bytes, without using the operation delta.
// The caller must first authenticate the graph using the Core SDK.
func (f *Fixture) ValidateGraphAgainstSource(view authenticationgraph.View, backend string, source map[string][]byte) error {
	if f == nil {
		return fmt.Errorf("RQ2 source fixture is nil")
	}
	profile, err := BackendProfile(backend)
	if err != nil {
		return err
	}
	descriptor, _, err := maltcid.ParseRoot(view.Root)
	if err != nil || descriptor.Profile != profile || descriptor.Layout != maltcid.Prefix || descriptor.InputRule != uint8(input.BytesSHA256) {
		return fmt.Errorf("source root requires Prefix AA1 for backend %s", backend)
	}
	root, err := view.State(view.Root)
	if err != nil {
		return err
	}
	if root.Descriptor != descriptor || root.ChunkSize != 0 || root.TotalSize != 0 || len(root.Entries) != len(source) {
		return fmt.Errorf("root metadata or binding count differs from source")
	}
	entries := make(map[string]engine.Entry, len(root.Entries))
	for _, entry := range root.Entries {
		if entry.Input.Kind != input.Label || entry.Input.Validate() != nil {
			return fmt.Errorf("root contains a non-label input")
		}
		path := string(entry.Input.Data)
		if err := validatePathCoordinate(path, path); err != nil {
			return err
		}
		if _, duplicate := entries[path]; duplicate {
			return fmt.Errorf("root repeats label %q", path)
		}
		entries[path] = entry
	}
	used := map[string]bool{view.Root.String(): true}
	for path, data := range source {
		entry, exists := entries[path]
		if !exists {
			return fmt.Errorf("source file %q is absent from authenticated root", path)
		}
		file, measured := f.List(path)
		if !measured {
			expected, err := clientcas.CIDForBlock(clientcas.Block{Codec: cid.Raw, Data: data})
			if err != nil || !entry.Target.Equals(expected) {
				return fmt.Errorf("direct source bytes for %q do not bind authenticated CID", path)
			}
			continue
		}
		descriptor, _, err := maltcid.ParseRoot(entry.Target)
		if err != nil || descriptor.Profile != profile || descriptor.Layout != maltcid.Positional || descriptor.InputRule != uint8(input.Direct) {
			return fmt.Errorf("source file %q requires a Positional AA0 Root for backend %s", path, backend)
		}
		object, err := view.State(entry.Target)
		if err != nil {
			return err
		}
		if object.Descriptor != descriptor || object.ChunkSize != file.ChunkSize || object.TotalSize != uint64(len(data)) {
			return fmt.Errorf("source file %q measured metadata does not match post-image", path)
		}
		if err := validateChunkEntries(object, data); err != nil {
			return fmt.Errorf("source file %q: %w", path, err)
		}
		used[entry.Target.String()] = true
	}
	if len(view.States) != len(used) {
		return fmt.Errorf("authentication graph contains %d objects, full source closure uses %d", len(view.States), len(used))
	}
	for key := range view.States {
		if !used[key] {
			return fmt.Errorf("authentication graph contains an unrelated Root %s", key)
		}
	}
	return nil
}
func validateChunkEntries(state engine.State, data []byte) error {
	if state.ChunkSize == 0 {
		return fmt.Errorf("chunk size is zero")
	}
	count := uint64(len(data)) / state.ChunkSize
	if uint64(len(data))%state.ChunkSize != 0 {
		count++
	}
	if uint64(len(state.Entries)) != count {
		return fmt.Errorf("object has %d chunks, source bytes require %d", len(state.Entries), count)
	}
	// Match by explicit index; input slice order is not an authenticated property.
	seen := make(map[uint64]bool, len(state.Entries))
	for _, entry := range state.Entries {
		index := entry.Input.Number
		if entry.Input.Kind != input.Index || entry.Input.Validate() != nil || index >= count || seen[index] {
			return fmt.Errorf("chunk input is duplicate, invalid or outside the measured sequence")
		}
		seen[index] = true
		start := index * state.ChunkSize
		end := start + min(uint64(len(data))-start, state.ChunkSize)
		expected, err := clientcas.CIDForBlock(clientcas.Block{Codec: cid.Raw, Data: data[start:end]})
		if err != nil || !entry.Target.Equals(expected) {
			return fmt.Errorf("chunk %d CID does not bind its source bytes", index)
		}
	}
	return nil
}

func BackendProfile(backend string) (maltcid.ProfileID, error) {
	switch backend {
	case "kzg":
		return maltcid.KZG4096, nil
	case "ipa":
		return maltcid.IPA256, nil
	default:
		return 0, fmt.Errorf("unsupported backend %q", backend)
	}
}
