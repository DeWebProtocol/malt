package daemon

import (
	"testing"
	"time"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/storage/cas/mock"
)

func TestEmbeddedMockCASOptionsDefaultToNoLatency(t *testing.T) {
	cfg := config.DefaultConfig()

	opts, err := embeddedMockCASOptions(cfg)
	if err != nil {
		t.Fatalf("embeddedMockCASOptions: %v", err)
	}
	cas := mock.NewCAS(opts...)

	start := time.Now()
	if _, err := cas.Put(t.Context(), []byte("fast local mock")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("default embedded mock Put took %s, want no artificial latency", elapsed)
	}
}
