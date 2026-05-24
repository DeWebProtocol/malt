// Package gateway provides temporary compatibility aliases for the writer
// mutation boundary.
package gateway

import (
	"context"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/writer"
)

var (
	ErrInvalidNamespace     = writer.ErrInvalidNamespace
	ErrInvalidBaseRoot      = writer.ErrInvalidBaseRoot
	ErrEmptyDeltas          = writer.ErrEmptyDeltas
	ErrObjectKindMismatch   = writer.ErrObjectKindMismatch
	ErrNilDelta             = writer.ErrNilDelta
	ErrExpectedRootMismatch = writer.ErrExpectedRootMismatch
)

type SemanticMutation = writer.SemanticMutation
type ArcSetDelta = writer.ArcSetDelta
type CommitDescriptor = writer.CommitDescriptor
type FixedListCommit = writer.FixedListCommit
type WriteReceipt = writer.WriteReceipt

// Executor is retained temporarily for callers that still import core/gateway.
type Executor struct {
	Namespace string
	Maps      mapping.Semantics
	Lists     list.Semantics
	ArcTable  arctable.ArcTable
}

// ValidateSemanticMutation forwards validation to the writer mutation model.
func ValidateSemanticMutation(mut SemanticMutation) error {
	return writer.ValidateSemanticMutation(mut)
}

// Apply forwards semantic mutation execution to writer.Writer.
func (e Executor) Apply(ctx context.Context, mut SemanticMutation) (WriteReceipt, error) {
	return writer.NewWriter(e.Maps, e.ArcTable, e.Lists).Apply(ctx, e.Namespace, mut)
}
