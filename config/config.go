// Package config provides configuration management for MALT.
// Configuration can be loaded from file, environment variables,
// command-line flags, or defaults.
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// Config holds all MALT configuration.
type Config struct {
	// Commitment scheme type: "mock", "kzg", "verkle", "ipa"
	CommitmentType string `json:"commitment_type"`

	// KVStore type: "memory", "badger"
	KVStoreType string `json:"kvstore_type"`

	// EAT type: "simple", "versioned"
	EATType string `json:"eat_type"`

	// CAS type: "mock", "ipfs-gateway"
	CASType string `json:"cas_type"`

	// Component-specific configs
	KVStore   KVStoreConfig   `json:"kvstore"`
	Commitment CommitmentConfig `json:"commitment"`
	CAS       CASConfig       `json:"cas"`
}

// KVStoreConfig holds KVStore-specific configuration.
type KVStoreConfig struct {
	// BadgerDB path (only used when type is "badger")
	Path string `json:"path"`

	// Run in memory mode
	InMemory bool `json:"in_memory"`
}

// CommitmentConfig holds commitment scheme configuration.
type CommitmentConfig struct {
	// Vector size for mock/IPA (default: 256)
	VectorSize int `json:"vector_size"`
}

// CASConfig holds CAS client configuration.
type CASConfig struct {
	// IPFS Gateway URL (only used when type is "ipfs-gateway")
	GatewayURL string `json:"gateway_url"`

	// HTTP timeout
	Timeout string `json:"timeout"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		CommitmentType: "mock",
		KVStoreType:    "memory",
		EATType:        "simple",
		CASType:        "mock",
		KVStore: KVStoreConfig{
			Path:     "./data/malt.db",
			InMemory: true,
		},
		Commitment: CommitmentConfig{
			VectorSize: 256,
		},
		CAS: CASConfig{
			GatewayURL: "https://ipfs.io/ipfs",
			Timeout:    "30s",
		},
	}
}

// Load loads configuration from file, environment, and flags.
// Priority: flags > env > file > defaults
func Load() (*Config, error) {
	cfg := DefaultConfig()

	// Parse flags
	var (
		configFile      = flag.String("config", "", "Path to config file")
		commitmentType  = flag.String("commitment", "", "Commitment type (mock/kzg/verkle/ipa)")
		kvstoreType     = flag.String("kvstore", "", "KVStore type (memory/badger)")
		eatType         = flag.String("eat", "", "EAT type (simple/versioned)")
		casType         = flag.String("cas", "", "CAS type (mock/ipfs-gateway)")
		kvstorePath     = flag.String("kv-path", "", "BadgerDB path")
		ipfsGateway     = flag.String("ipfs-gateway", "", "IPFS gateway URL")
	)
	flag.Parse()

	// Load from file if specified
	if *configFile != "" {
		if err := loadFromFile(cfg, *configFile); err != nil {
			return nil, fmt.Errorf("failed to load config file: %w", err)
		}
	}

	// Load from environment
	loadFromEnv(cfg)

	// Override with flags
	if *commitmentType != "" {
		cfg.CommitmentType = *commitmentType
	}
	if *kvstoreType != "" {
		cfg.KVStoreType = *kvstoreType
	}
	if *eatType != "" {
		cfg.EATType = *eatType
	}
	if *casType != "" {
		cfg.CASType = *casType
	}
	if *kvstorePath != "" {
		cfg.KVStore.Path = *kvstorePath
		cfg.KVStore.InMemory = false
	}
	if *ipfsGateway != "" {
		cfg.CAS.GatewayURL = *ipfsGateway
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loadFromFile loads configuration from a JSON file.
func loadFromFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, cfg)
}

// loadFromEnv loads configuration from environment variables.
func loadFromEnv(cfg *Config) {
	if v := os.Getenv("MALT_COMMITMENT"); v != "" {
		cfg.CommitmentType = v
	}
	if v := os.Getenv("MALT_KVSTORE"); v != "" {
		cfg.KVStoreType = v
	}
	if v := os.Getenv("MALT_EAT"); v != "" {
		cfg.EATType = v
	}
	if v := os.Getenv("MALT_CAS"); v != "" {
		cfg.CASType = v
	}
	if v := os.Getenv("MALT_KV_PATH"); v != "" {
		cfg.KVStore.Path = v
		cfg.KVStore.InMemory = false
	}
	if v := os.Getenv("MALT_IPFS_GATEWAY"); v != "" {
		cfg.CAS.GatewayURL = v
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	validCommitment := map[string]bool{"mock": true, "kzg": true, "verkle": true, "ipa": true}
	if !validCommitment[c.CommitmentType] {
		return fmt.Errorf("invalid commitment type: %s (valid: mock, kzg, verkle, ipa)", c.CommitmentType)
	}

	validKV := map[string]bool{"memory": true, "badger": true}
	if !validKV[c.KVStoreType] {
		return fmt.Errorf("invalid kvstore type: %s (valid: memory, badger)", c.KVStoreType)
	}

	validEAT := map[string]bool{"simple": true, "versioned": true}
	if !validEAT[c.EATType] {
		return fmt.Errorf("invalid eat type: %s (valid: simple, versioned)", c.EATType)
	}

	validCAS := map[string]bool{"mock": true, "ipfs-gateway": true}
	if !validCAS[c.CASType] {
		return fmt.Errorf("invalid cas type: %s (valid: mock, ipfs-gateway)", c.CASType)
	}

	if c.Commitment.VectorSize <= 0 {
		return fmt.Errorf("vector size must be positive, got %d", c.Commitment.VectorSize)
	}

	return nil
}

// CASTimeout parses and returns the CAS timeout duration.
func (c *Config) CASTimeout() (time.Duration, error) {
	return time.ParseDuration(c.CAS.Timeout)
}

// String returns a string representation of the config.
func (c *Config) String() string {
	return fmt.Sprintf("Config{commitment=%s, kv=%s, eat=%s, cas=%s}",
		c.CommitmentType, c.KVStoreType, c.EATType, c.CASType)
}