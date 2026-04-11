package sidecar

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.IPFSAPIURL != "http://localhost:5001" {
		t.Errorf("expected IPFSAPIURL http://localhost:5001, got %s", cfg.IPFSAPIURL)
	}
	if cfg.HTTPAddr != "localhost:8082" {
		t.Errorf("expected HTTPAddr localhost:8082, got %s", cfg.HTTPAddr)
	}
	if cfg.KVStoreType != "memory" {
		t.Errorf("expected KVStoreType memory, got %s", cfg.KVStoreType)
	}
	if cfg.CommitmentType != "kzg" {
		t.Errorf("expected CommitmentType kzg, got %s", cfg.CommitmentType)
	}
	if cfg.EATType != "overwrite" {
		t.Errorf("expected EATType overwrite, got %s", cfg.EATType)
	}
}

func TestNewSidecarDefaults(t *testing.T) {
	sc, err := NewSidecar(Config{})
	if err != nil {
		t.Fatalf("NewSidecar failed: %v", err)
	}
	defer sc.Close()

	if sc.node == nil {
		t.Error("sidecar node should not be nil")
	}
	if sc.server == nil {
		t.Error("sidecar server should not be nil")
	}
	if sc.server.Addr != "localhost:8082" {
		t.Errorf("expected server addr localhost:8082, got %s", sc.server.Addr)
	}
}

func TestNewSidecarCustomConfig(t *testing.T) {
	sc, err := NewSidecar(Config{
		IPFSAPIURL:     "http://127.0.0.1:5001",
		HTTPAddr:       "localhost:9999",
		CommitmentType: "verkle",
		EATType:        "versioned",
	})
	if err != nil {
		t.Fatalf("NewSidecar failed: %v", err)
	}
	defer sc.Close()

	if sc.cfg.IPFSAPIURL != "http://127.0.0.1:5001" {
		t.Errorf("expected IPFSAPIURL http://127.0.0.1:5001, got %s", sc.cfg.IPFSAPIURL)
	}
	if sc.server.Addr != "localhost:9999" {
		t.Errorf("expected server addr localhost:9999, got %s", sc.server.Addr)
	}
}

func TestSidecarNode(t *testing.T) {
	sc, err := NewSidecar(DefaultConfig())
	if err != nil {
		t.Fatalf("NewSidecar failed: %v", err)
	}
	defer sc.Close()

	node := sc.Node()
	if node == nil {
		t.Error("Node() should not return nil")
	}
}
