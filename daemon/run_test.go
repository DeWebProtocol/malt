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

func TestNewEmbeddedMockCASPersistsBlocksUnderStateRoot(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()

	first, closeFirst, err := newEmbeddedMockCAS(cfg)
	if err != nil {
		t.Fatalf("new first embedded mock CAS: %v", err)
	}
	block, err := first.Put(t.Context(), []byte("persistent payload"))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}
	if err := closeFirst(); err != nil {
		t.Fatalf("close first embedded mock CAS: %v", err)
	}

	second, closeSecond, err := newEmbeddedMockCAS(cfg)
	if err != nil {
		t.Fatalf("new second embedded mock CAS: %v", err)
	}
	defer closeSecond()

	got, err := second.Get(t.Context(), block)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if string(got) != "persistent payload" {
		t.Fatalf("payload = %q, want persistent payload", got)
	}
}
