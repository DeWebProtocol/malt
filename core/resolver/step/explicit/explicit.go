// Package explicit implements the Step interface for MALT explicit arcs.
// It uses longest-prefix matching in ArcTable and generates cryptographic proof via
// keyed-map semantics.
package explicit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/resolver/step"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Reserved arc paths for MALT structures
const (
	// PayloadArc is the reserved path that binds a structure root to its payload CID.
	// When resolving a MALT object with an empty path, the upper resolver loop
	// redirects to this arc to materialize the payload.
	PayloadArc arcset.Path = "@payload"
)

// Resolver resolves explicit MALT arcs using longest-prefix matching.
type Resolver struct {
	arctable arctable.ArcTable
	semantic mapping.Semantics
	bucketId string
}

// NewResolver creates a new explicit arc resolver.
func NewResolver(e arctable.ArcTable, semantic mapping.Semantics, bucketId string) *Resolver {
	return &Resolver{
		arctable: e,
		semantic: semantic,
		bucketId: bucketId,
	}
}

// Resolve finds the longest matching prefix in the ArcTable and generates proof.
// Returns: matchedPath, target, evidence, error
//
// Example: if ArcTable contains "a/b/c" → key1 and path is "a/b/c/d/e",
// it matches "a/b/c" and returns that path with its target and evidence.
func (r *Resolver) Resolve(root cid.Cid, path arcset.Path) (matchedPath arcset.Path, target cid.Cid, ev evidence.Evidence, err error) {
	start := time.Now()

	logger.Debug("Resolver.Resolve started",
		logger.String("bucket", r.bucketId),
		logger.String("root", root.String()),
		logger.String("path", path.String()))

	if !root.Defined() {
		logger.Error("Resolver.Resolve root not defined")
		return "", cid.Cid{}, nil, fmt.Errorf("root is not defined")
	}
	if path.IsEmpty() {
		logger.Error("Resolver.Resolve path is empty")
		return "", cid.Cid{}, nil, fmt.Errorf("path is empty")
	}

	ctx := context.Background()

	// Try to find the longest matching prefix
	segments := splitPath(path)

	// Try from longest to shortest
	for i := len(segments); i > 0; i-- {
		candidatePath := strings.Join(segments[:i], "/")

		target, err := r.arctable.Get(ctx, r.bucketId, root, candidatePath)
		if err == nil {
			// Found a match, generate proof
			binding, proof, err := r.semantic.Prove(ctx, r.bucketId, root, arcset.CanonicalizePath(candidatePath))
			if err != nil {
				logger.Error("Resolver.Resolve prove failed",
					logger.String("path", candidatePath),
					logger.Err(err))
				return "", cid.Cid{}, nil, fmt.Errorf("failed to generate proof: %w", err)
			}

			logger.Info("Resolver.Resolve completed",
				logger.String("bucket", r.bucketId),
				logger.String("root", root.String()),
				logger.String("requested_path", path.String()),
				logger.String("matched_path", candidatePath),
				logger.String("target", target.String()),
				logger.Int("segment_depth", len(segments)-i),
				logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

			if !binding.Present || !binding.Value.Equals(target) {
				return "", cid.Cid{}, nil, fmt.Errorf("semantic proof binding mismatch for path: %s", candidatePath)
			}
			return arcset.Path(candidatePath), target, evidence.NewExplicitEvidence(proof), nil
		}
	}

	logger.Warn("Resolver.Resolve no match found",
		logger.String("bucket", r.bucketId),
		logger.String("root", root.String()),
		logger.String("path", path.String()))

	return "", cid.Cid{}, nil, fmt.Errorf("no matching arc found for path: %s", path)
}

// Verify verifies a single step's evidence.
func (r *Resolver) Verify(root cid.Cid, path arcset.Path, target cid.Cid, ev evidence.Evidence) (bool, error) {
	start := time.Now()

	logger.Debug("Resolver.Verify started",
		logger.String("bucket", r.bucketId),
		logger.String("root", root.String()),
		logger.String("path", path.String()))

	if ev == nil {
		logger.Error("Resolver.Verify evidence is nil")
		return false, fmt.Errorf("evidence is nil")
	}

	explicitEv, ok := ev.(*evidence.ExplicitEvidence)
	if !ok {
		logger.Error("Resolver.Verify wrong evidence type",
			logger.String("type", fmt.Sprintf("%T", ev)))
		return false, fmt.Errorf("expected ExplicitEvidence, got %T", ev)
	}

	valid, err := r.semantic.Verify(root, path, mapping.Binding{Value: target, Present: true}, explicitEv.Bytes())
	if err != nil {
		logger.Error("Resolver.Verify semantic verification failed",
			logger.String("path", path.String()),
			logger.Err(err))
		return false, err
	}

	logger.Debug("Resolver.Verify completed",
		logger.String("bucket", r.bucketId),
		logger.String("path", path.String()),
		logger.Bool("valid", valid),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return valid, nil
}

// splitPath splits a path into segments.
func splitPath(path arcset.Path) []string {
	return step.SplitPath(path)
}
