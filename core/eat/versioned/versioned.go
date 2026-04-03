// Package versioned provides a versioned EAT implementation using a KVStore.
// Each version stores only modified arcs plus a @previous arc pointing to the parent version.
// Resolution walks the @previous chain to find arc entries.
package versioned

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/kvstore"
	cid "github.com/ipfs/go-cid"
)

// Reserved arc paths
const (
	PreviousArc = "@previous" // Points to parent version's commitment root
)

// EAT is a single-graph versioned EAT implementation.
// Each version stores only modified arcs, with @previous linking to the parent.
type EAT struct {
	mu      sync.RWMutex
	graphId string
	kv      kvstore.KVStore
	current cid.Cid // current/latest commitment root
}

// NewEAT creates a new versioned EAT with the given KVStore and graphId.
func NewEAT(kv kvstore.KVStore, graphId string) (*EAT, error) {
	if kv == nil {
		return nil, fmt.Errorf("KVStore is required")
	}
	if graphId == "" {
		return nil, fmt.Errorf("graphId is required")
	}

	return &EAT{
		graphId: graphId,
		kv:      kv,
	}, nil
}

// arcKey generates the storage key for a version and path.
// Format: graphId:version:path
func (e *EAT) arcKey(version cid.Cid, path string) []byte {
	return []byte(e.graphId + ":" + version.String() + ":" + path)
}

// versionPrefix generates the prefix for all arcs of a specific version.
// Format: graphId:version:
func (e *EAT) versionPrefix(version cid.Cid) []byte {
	return []byte(e.graphId + ":" + version.String() + ":")
}

// Get retrieves the target CID for a path at a specific version.
// It walks the @previous chain starting from the given version until finding the arc.
// Returns ErrNotFound if the path doesn't exist in any ancestor version.
func (e *EAT) Get(version cid.Cid, path string) (cid.Cid, error) {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	currentVersion := version
	maxDepth := 1000 // Prevent infinite loops

	for i := 0; i < maxDepth; i++ {
		// Try to get the arc at current version
		key := e.arcKey(currentVersion, path)
		val, err := e.kv.Get(ctx, key)
		if err == nil {
			// Found the arc
			return cid.Cast(val)
		}

		if err != kvstore.ErrNotFound {
			return cid.Cid{}, fmt.Errorf("failed to get arc: %w", err)
		}

		// Arc not found at this version, try parent via @previous
		prevKey := e.arcKey(currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			if err == kvstore.ErrNotFound {
				// No parent, arc doesn't exist
				return cid.Cid{}, arcset.ErrNotFound
			}
			return cid.Cid{}, fmt.Errorf("failed to get @previous: %w", err)
		}

		// Move to parent version
		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			return cid.Cid{}, fmt.Errorf("invalid @previous CID: %w", err)
		}
		currentVersion = parentVersion
	}

	return cid.Cid{}, fmt.Errorf("version chain too deep (max %d)", maxDepth)
}

// GetLatest retrieves the target CID for a path at the current (latest) version.
func (e *EAT) GetLatest(path string) (cid.Cid, error) {
	e.mu.RLock()
	current := e.current
	e.mu.RUnlock()

	if current == cid.Undef {
		return cid.Cid{}, arcset.ErrNotFound
	}

	return e.Get(current, path)
}

// Set stores an arc entry at a specific version.
// This is used when creating a new version with modified arcs.
// The caller must ensure the version is properly linked via @previous.
func (e *EAT) Set(version cid.Cid, path string, target cid.Cid) error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	key := e.arcKey(version, path)
	val := target.Bytes()

	if err := e.kv.Put(ctx, key, val); err != nil {
		return fmt.Errorf("failed to set arc: %w", err)
	}

	return nil
}

// SetBatch stores multiple arc entries at a specific version in a single transaction.
func (e *EAT) SetBatch(version cid.Cid, arcs map[string]cid.Cid) error {
	if len(arcs) == 0 {
		return nil
	}

	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	batch := e.kv.Batch()
	for path, target := range arcs {
		key := e.arcKey(version, path)
		val := target.Bytes()
		if err := batch.Put(key, val); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to add arc to batch: %w", err)
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit batch: %w", err)
	}

	return nil
}

// Update stores arcs at a new version and links it to the parent version.
// The newRoot becomes the new version identifier, linked to parentRoot via @previous.
// If parentRoot is cid.Undef, this creates the first version (no @previous).
func (e *EAT) Update(newRoot, parentRoot cid.Cid, arcs map[string]cid.Cid) error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	batch := e.kv.Batch()

	// Store all arcs for this version
	for path, target := range arcs {
		key := e.arcKey(newRoot, path)
		val := target.Bytes()
		if err := batch.Put(key, val); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to add arc to batch: %w", err)
		}
	}

	// Link to parent via @previous (unless this is the first version)
	if parentRoot != cid.Undef {
		prevKey := e.arcKey(newRoot, PreviousArc)
		prevVal := parentRoot.Bytes()
		if err := batch.Put(prevKey, prevVal); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to add @previous to batch: %w", err)
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit version: %w", err)
	}

	// Update current to new version
	e.current = newRoot

	return nil
}

// Current returns the current (latest) commitment root.
func (e *EAT) Current() cid.Cid {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.current
}

// GetParent returns the parent version of a given version via @previous.
// Returns cid.Undef if the version has no parent (first version).
func (e *EAT) GetParent(version cid.Cid) (cid.Cid, error) {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prevKey := e.arcKey(version, PreviousArc)
	prevVal, err := e.kv.Get(ctx, prevKey)
	if err != nil {
		if err == kvstore.ErrNotFound {
			return cid.Undef, nil // No parent
		}
		return cid.Cid{}, fmt.Errorf("failed to get @previous: %w", err)
	}

	return cid.Cast(prevVal)
}

// View returns an ArcSetView for a specific version.
// The view walks the @previous chain for Get operations.
func (e *EAT) View(version cid.Cid) arcset.View {
	return &versionedView{eat: e, version: version}
}

// ViewLatest returns an ArcSetView for the current (latest) version.
func (e *EAT) ViewLatest() arcset.View {
	e.mu.RLock()
	current := e.current
	e.mu.RUnlock()

	return &versionedView{eat: e, version: current}
}

// Iterate returns an iterator over all arcs directly stored at a specific version.
// Note: This only iterates arcs at that version, not including ancestors.
func (e *EAT) Iterate(version cid.Cid) arcset.Iterator {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prefix := e.versionPrefix(version)
	iter := e.kv.NewIterator(ctx, prefix, nil)

	return &versionedIterator{
		kv:     e.kv,
		iter:   iter,
		prefix: prefix,
	}
}

// IterateLatest returns an iterator over arcs at the current version.
func (e *EAT) IterateLatest() arcset.Iterator {
	e.mu.RLock()
	current := e.current
	e.mu.RUnlock()

	if current == cid.Undef {
		return &emptyIterator{}
	}

	return e.Iterate(current)
}

// Len returns the number of arcs directly stored at a specific version.
// Note: This only counts arcs at that version, not including ancestors.
func (e *EAT) Len(version cid.Cid) int {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	prefix := e.versionPrefix(version)
	iter := e.kv.NewIterator(ctx, prefix, nil)
	defer iter.Close()

	count := 0
	for iter.Next() {
		key := iter.Key()
		// Extract path and skip @previous
		path := string(key[len(prefix):])
		if path == PreviousArc {
			continue
		}
		count++
	}

	return count
}

// TotalLen returns the total number of arcs visible at a version (including ancestors).
// This walks the @previous chain and collects all unique paths.
func (e *EAT) TotalLen(version cid.Cid) int {
	ctx := context.Background()

	e.mu.RLock()
	defer e.mu.RUnlock()

	seenPaths := make(map[string]bool)
	currentVersion := version
	maxDepth := 1000

	for i := 0; i < maxDepth; i++ {
		prefix := e.versionPrefix(currentVersion)
		iter := e.kv.NewIterator(ctx, prefix, nil)

		for iter.Next() {
			key := iter.Key()
			path := string(key[len(prefix):])
			if path != PreviousArc {
				seenPaths[path] = true
			}
		}
		iter.Close()

		// Try to get parent
		prevKey := e.arcKey(currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			break // No more ancestors
		}

		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			break
		}
		currentVersion = parentVersion
	}

	return len(seenPaths)
}

// Delete removes an arc entry at a specific version.
// Note: In versioned EAT, delete typically means setting to a special "deleted" marker
// or creating a new version without that arc. Direct deletion is rarely used.
func (e *EAT) Delete(version cid.Cid, path string) error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	key := e.arcKey(version, path)

	if err := e.kv.Delete(ctx, key); err != nil {
		return fmt.Errorf("failed to delete arc: %w", err)
	}

	return nil
}

// Close releases resources.
func (e *EAT) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.current = cid.Undef
	return nil
}

// GraphId returns the graph identifier.
func (e *EAT) GraphId() string {
	return e.graphId
}

// CopyOnWrite copies unchanged arcs from parent to a new version.
// This shortens resolution paths by making all arcs available at the new version.
// Use this when version history is long and resolution cost needs optimization.
func (e *EAT) CopyOnWrite(newRoot cid.Cid, parentRoot cid.Cid, modifiedPaths map[string]bool) error {
	ctx := context.Background()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Collect all arcs from ancestors that weren't modified
	arcsToCopy := make(map[string]cid.Cid)
	currentVersion := parentRoot
	maxDepth := 1000

	for i := 0; i < maxDepth; i++ {
		prefix := e.versionPrefix(currentVersion)
		iter := e.kv.NewIterator(ctx, prefix, nil)

		for iter.Next() {
			key := iter.Key()
			path := string(key[len(prefix):])

			// Skip @previous and modified paths
			if path == PreviousArc || modifiedPaths[path] {
				continue
			}

			// If we haven't seen this path, add it
			if _, seen := arcsToCopy[path]; !seen {
				val := iter.Value()
				c, err := cid.Cast(val)
				if err == nil {
					arcsToCopy[path] = c
				}
			}
		}
		iter.Close()

		// Get parent version
		prevKey := e.arcKey(currentVersion, PreviousArc)
		prevVal, err := e.kv.Get(ctx, prevKey)
		if err != nil {
			break
		}

		parentVersion, err := cid.Cast(prevVal)
		if err != nil {
			break
		}
		currentVersion = parentVersion
	}

	// Copy arcs to new version
	if len(arcsToCopy) == 0 {
		return nil
	}

	batch := e.kv.Batch()
	for path, target := range arcsToCopy {
		key := e.arcKey(newRoot, path)
		val := target.Bytes()
		if err := batch.Put(key, val); err != nil {
			batch.Cancel()
			return fmt.Errorf("failed to copy arc: %w", err)
		}
	}

	if err := batch.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit copied arcs: %w", err)
	}

	return nil
}

// versionedView implements arcset.View for a specific version.
type versionedView struct {
	eat     *EAT
	version cid.Cid
}

func (v *versionedView) Get(path string) (cid.Cid, bool) {
	c, err := v.eat.Get(v.version, path)
	if err != nil {
		return cid.Cid{}, false
	}
	return c, true
}

func (v *versionedView) Iterate() arcset.Iterator {
	// For versioned view, we provide a flattened iterator that walks ancestors
	return &flattenedIterator{
		eat:     v.eat,
		version: v.version,
		seen:    make(map[string]bool),
	}
}

func (v *versionedView) Len() int {
	return v.eat.TotalLen(v.version)
}

// versionedIterator implements arcset.Iterator for arcs at a single version.
type versionedIterator struct {
	kv     kvstore.KVStore
	iter   kvstore.Iterator
	prefix []byte
}

func (it *versionedIterator) Next() (string, cid.Cid, bool) {
	if !it.iter.Next() {
		return "", cid.Cid{}, false
	}

	key := it.iter.Key()
	path := string(key[len(it.prefix):])

	// Skip @previous
	if path == PreviousArc {
		return it.Next()
	}

	val := it.iter.Value()
	c, err := cid.Cast(val)
	if err != nil {
		return it.Next() // Skip invalid entries
	}

	return path, c, true
}

func (it *versionedIterator) Err() error {
	return it.iter.Err()
}

// flattenedIterator walks the @previous chain to iterate all visible arcs.
type flattenedIterator struct {
	eat       *EAT
	version   cid.Cid
	seen      map[string]bool
	current   map[string]cid.Cid // arcs at current version being processed
	paths     []string           // sorted paths at current version
	pathIndex int
	parent    cid.Cid // next parent version to process
	err       error
}

func (it *flattenedIterator) Next() (string, cid.Cid, bool) {
	// Try to return from current version's arcs
	for it.pathIndex < len(it.paths) {
		path := it.paths[it.pathIndex]
		it.pathIndex++

		if it.seen[path] {
			continue
		}
		it.seen[path] = true
		return path, it.current[path], true
	}

	// Load next version if available
	if it.parent == cid.Undef && it.version == cid.Undef {
		return "", cid.Cid{}, false
	}

	ctx := context.Background()
	var nextVersion cid.Cid
	if it.parent != cid.Undef {
		nextVersion = it.parent
		it.parent = cid.Undef
	} else if it.version != cid.Undef {
		nextVersion = it.version
		it.version = cid.Undef
	}

	// Load arcs at this version
	it.current = make(map[string]cid.Cid)
	prefix := it.eat.versionPrefix(nextVersion)
	iter := it.eat.kv.NewIterator(ctx, prefix, nil)

	for iter.Next() {
		key := iter.Key()
		path := string(key[len(prefix):])
		if path == PreviousArc {
			val := iter.Value()
			p, err := cid.Cast(val)
			if err == nil {
				it.parent = p
			}
			continue
		}
		val := iter.Value()
		c, err := cid.Cast(val)
		if err == nil && !it.seen[path] {
			it.current[path] = c
		}
	}
	iter.Close()

	// Sort paths
	it.paths = make([]string, 0, len(it.current))
	for p := range it.current {
		it.paths = append(it.paths, p)
	}
	sort.Strings(it.paths)
	it.pathIndex = 0

	// Try again
	return it.Next()
}

func (it *flattenedIterator) Err() error {
	return it.err
}

// emptyIterator is an empty iterator.
type emptyIterator struct{}

func (it *emptyIterator) Next() (string, cid.Cid, bool) {
	return "", cid.Cid{}, false
}

func (it *emptyIterator) Err() error {
	return nil
}