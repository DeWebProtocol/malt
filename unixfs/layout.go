package unixfs

import (
	"context"
	"fmt"
	"strings"
)

// LayoutKind is the stable application-level identifier persisted by callers.
// It is independent of the MALT root codec and commitment backend.
type LayoutKind string

const (
	LayoutFlatV1   LayoutKind = "flat-v1"
	LayoutHybridV1 LayoutKind = "hybrid-v1"
)

// Layout materializes a staged UnixFS tree without changing Core proof or
// commitment semantics. Implementations decide how directory manifests and
// path bindings are projected into the current structured MALT Map root.
type Layout interface {
	Kind() LayoutKind
	Materialize(context.Context, StagedRootCreator, StagedBlockStore, *StagedNode) (*StagedMaterializeResult, error)
}

// ParseLayoutKind validates a persisted layout identifier. Empty values are
// deliberately rejected so compatibility defaults remain caller-owned.
func ParseLayoutKind(raw string) (LayoutKind, error) {
	kind := LayoutKind(strings.ToLower(strings.TrimSpace(raw)))
	switch kind {
	case LayoutFlatV1, LayoutHybridV1:
		return kind, nil
	default:
		return "", fmt.Errorf("unsupported MALT UnixFS layout %q", raw)
	}
}

// NewLayout returns the application implementation selected by kind.
func NewLayout(kind LayoutKind) (Layout, error) {
	switch kind {
	case LayoutFlatV1:
		return flatLayout{}, nil
	case LayoutHybridV1:
		return hybridLayout{}, nil
	default:
		return nil, fmt.Errorf("unsupported MALT UnixFS layout %q", kind)
	}
}

type flatLayout struct{}

func (flatLayout) Kind() LayoutKind { return LayoutFlatV1 }

func (flatLayout) Materialize(ctx context.Context, roots StagedRootCreator, blocks StagedBlockStore, node *StagedNode) (*StagedMaterializeResult, error) {
	return materializeFlatDirectory(ctx, roots, blocks, node)
}

type hybridLayout struct{}

func (hybridLayout) Kind() LayoutKind { return LayoutHybridV1 }

func (hybridLayout) Materialize(ctx context.Context, roots StagedRootCreator, blocks StagedBlockStore, node *StagedNode) (*StagedMaterializeResult, error) {
	return materializeHybridDirectory(ctx, roots, blocks, node)
}
