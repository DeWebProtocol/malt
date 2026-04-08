// Package explicit implements the Resolver interface for MALT explicit arcs.
// It uses longest-prefix matching in EAT and generates cryptographic proof via SCE.
package explicit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/logger"
	cid "github.com/ipfs/go-cid"
)

// Reserved arc paths for MALT structures
const (
	// PayloadArc is the reserved path that binds a structure root to its payload CID.
	// When resolving a MALT object with an empty path, Gateway automatically redirects
	// to this arc to materialize the payload.
	PayloadArc = "@payload"
)

// Resolver resolves explicit MALT arcs using longest-prefix matching.
type Resolver struct {
	eat      eat.EAT
	sce      *sce.Engine
	bucketId string
}

// NewResolver creates a new explicit arc resolver.
func NewResolver(e eat.EAT, s *sce.Engine, bucketId string) *Resolver {
	return &Resolver{
		eat:      e,
		sce:      s,
		bucketId: bucketId,
	}
}

// Resolve finds the longest matching prefix in the EAT and generates proof.
// Returns: matchedPath, target, evidence, error
//
// Example: if EAT contains "a/b/c" → key1 and path is "a/b/c/d/e",
// it matches "a/b/c" and returns that path with its target and evidence.
func (r *Resolver) Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error) {
	start := time.Now()

	logger.Debug("Resolver.Resolve started",
		logger.String("bucket", r.bucketId),
		logger.String("root", root.String()),
		logger.String("path", path))

	if !root.Defined() {
		logger.Error("Resolver.Resolve root not defined")
		return "", cid.Cid{}, nil, fmt.Errorf("root is not defined")
	}
	if path == "" {
		logger.Error("Resolver.Resolve path is empty")
		return "", cid.Cid{}, nil, fmt.Errorf("path is empty")
	}

	ctx := context.Background()

	// Try to find the longest matching prefix
	segments := splitPath(path)

	// Try from longest to shortest
	for i := len(segments); i > 0; i-- {
		candidatePath := strings.Join(segments[:i], "/")

		target, err := r.eat.Get(ctx, r.bucketId, root, candidatePath)
		if err == nil {
			// Found a match, generate proof
			snapshot, err := r.eat.Snapshot(ctx, r.bucketId, root)
			if err != nil {
				logger.Error("Resolver.Resolve snapshot failed",
					logger.String("path", candidatePath),
					logger.Err(err))
				return "", cid.Cid{}, nil, fmt.Errorf("failed to get snapshot: %w", err)
			}
			_, proof, err := r.sce.Prove(root, snapshot, candidatePath)
			if err != nil {
				logger.Error("Resolver.Resolve prove failed",
					logger.String("path", candidatePath),
					logger.Err(err))
				return "", cid.Cid{}, nil, fmt.Errorf("failed to generate proof: %w", err)
			}

			logger.Info("Resolver.Resolve completed",
				logger.String("bucket", r.bucketId),
				logger.String("root", root.String()),
				logger.String("requested_path", path),
				logger.String("matched_path", candidatePath),
				logger.String("target", target.String()),
				logger.Int("segment_depth", len(segments)-i),
				logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

			return candidatePath, target, evidence.NewExplicitEvidence(proof), nil
		}
	}

	logger.Warn("Resolver.Resolve no match found",
		logger.String("bucket", r.bucketId),
		logger.String("root", root.String()),
		logger.String("path", path))

	return "", cid.Cid{}, nil, fmt.Errorf("no matching arc found for path: %s", path)
}

// Verify verifies a single step's evidence.
func (r *Resolver) Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error) {
	start := time.Now()

	logger.Debug("Resolver.Verify started",
		logger.String("bucket", r.bucketId),
		logger.String("root", root.String()),
		logger.String("path", path))

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

	valid, err := r.sce.Verify(root, path, target, explicitEv.Bytes())
	if err != nil {
		logger.Error("Resolver.Verify SCE failed",
			logger.String("path", path),
			logger.Err(err))
		return false, err
	}

	logger.Debug("Resolver.Verify completed",
		logger.String("bucket", r.bucketId),
		logger.String("path", path),
		logger.Bool("valid", valid),
		logger.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000))

	return valid, nil
}

// splitPath splits a path into segments.
func splitPath(path string) []string {
	if path == "" {
		return nil
	}

	// Remove leading slash
	if path[0] == '/' {
		path = path[1:]
	}

	if path == "" {
		return nil
	}

	return strings.Split(path, "/")
}