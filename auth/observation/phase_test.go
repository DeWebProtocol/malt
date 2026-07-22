package observation

import (
	"context"
	"testing"
)

type collector struct{ samples []Sample }

func (c *collector) ObservePhase(sample Sample) { c.samples = append(c.samples, sample) }

func TestRequestScopedObservation(t *testing.T) {
	value := new(collector)
	ctx := WithObserver(context.Background(), value)
	if !Enabled(ctx) || Enabled(context.Background()) {
		t.Fatal("observer enabled state does not follow request context")
	}
	done := Start(ctx, PhaseOpen)
	done(2, 3, 4)
	Record(ctx, Sample{Phase: PhaseSerialization, DurationNS: 5, Operations: 1, Bytes: 6})
	if len(value.samples) != 2 || value.samples[0].Phase != PhaseOpen || value.samples[0].Operations != 2 || value.samples[0].Items != 3 || value.samples[0].Bytes != 4 || value.samples[1].DurationNS != 5 {
		t.Fatalf("samples = %#v", value.samples)
	}
	Start(context.Background(), PhaseArcTable)(1, 1, 1)
}
