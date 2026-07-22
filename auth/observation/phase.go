// Package observation provides request-scoped, optional execution phase
// observations. Observations are diagnostics only: they are never proof
// evidence and cannot affect protocol results.
package observation

import (
	"context"
	"time"
)

// Phase names the stable server-side phases used by the paper evaluator.
type Phase string

const (
	PhaseArcTable        Phase = "arc-table"
	PhaseMaterialization Phase = "materialization"
	PhaseOpen            Phase = "open"
	PhaseSerialization   Phase = "serialization"
)

// Sample is one completed phase observation. Operations counts logical calls;
// Items and Bytes describe materialized or serialized data when known.
type Sample struct {
	Phase      Phase
	DurationNS uint64
	Operations uint64
	Items      uint64
	Bytes      uint64
}

// Observer consumes synchronous request-scoped samples. Implementations must
// not retain or mutate protocol inputs.
type Observer interface {
	ObservePhase(Sample)
}

type contextKey struct{}

// WithObserver attaches an optional observer to ctx.
func WithObserver(ctx context.Context, observer Observer) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, contextKey{}, observer)
}

// Enabled reports whether ctx carries a phase observer. Hot paths use this to
// avoid computing optional volume diagnostics when observations are disabled.
func Enabled(ctx context.Context) bool {
	observer, _ := ctx.Value(contextKey{}).(Observer)
	return observer != nil
}

// Start begins a phase span. The returned closure is allocation-free when no
// observer is attached and may be called exactly once with observed volume.
func Start(ctx context.Context, phase Phase) func(operations, items, bytes uint64) {
	observer, _ := ctx.Value(contextKey{}).(Observer)
	if observer == nil {
		return func(uint64, uint64, uint64) {}
	}
	started := time.Now()
	return func(operations, items, bytes uint64) {
		duration := time.Since(started)
		var nanos uint64
		if duration > 0 {
			nanos = uint64(duration)
		}
		observer.ObservePhase(Sample{Phase: phase, DurationNS: nanos, Operations: operations, Items: items, Bytes: bytes})
	}
}

// Record adds a phase sample measured by an outer boundary such as wire
// serialization.
func Record(ctx context.Context, sample Sample) {
	observer, _ := ctx.Value(contextKey{}).(Observer)
	if observer != nil {
		observer.ObservePhase(sample)
	}
}
