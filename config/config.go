// Package config provides configuration management for MALT using Viper.
// Configuration can be loaded from file, environment variables,
// command-line flags, or defaults.
package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Config holds all MALT configuration.
type Config struct {
	// Commitment scheme type: "mock", "kzg", "verkle", "ipa"
	CommitmentType string `mapstructure:"commitment_type"`

	// KVStore type: "memory", "badger"
	KVStoreType string `mapstructure:"kvstore_type"`

	// EAT type: "simple", "versioned"
	EATType string `mapstructure:"eat_type"`

	// CAS type: "mock", "ipfs-gateway"
	CASType string `mapstructure:"cas_type"`

	// Component-specific configs
	KVStore    KVStoreConfig    `mapstructure:"kvstore"`
	Commitment CommitmentConfig `mapstructure:"commitment"`
	CAS        CASConfig        `mapstructure:"cas"`
}

// KVStoreConfig holds KVStore-specific configuration.
type KVStoreConfig struct {
	// BadgerDB path (only used when type is "badger")
	Path string `mapstructure:"path"`

	// Run in memory mode
	InMemory bool `mapstructure:"in_memory"`
}

// CommitmentConfig holds commitment scheme configuration.
type CommitmentConfig struct {
	// Vector size for mock/IPA (default: 256)
	VectorSize int `mapstructure:"vector_size"`
}

// CASConfig holds CAS client configuration.
type CASConfig struct {
	// IPFS Gateway URL (only used when type is "ipfs-gateway")
	GatewayURL string `mapstructure:"gateway_url"`

	// HTTP timeout
	Timeout string `mapstructure:"timeout"`
}

// Init initializes Viper with default values and binds flags.
// This should be called after cobra flags are defined.
func Init() {
	// Set default values
	viper.SetDefault("commitment_type", "mock")
	viper.SetDefault("kvstore_type", "memory")
	viper.SetDefault("eat_type", "simple")
	viper.SetDefault("cas_type", "mock")
	viper.SetDefault("kvstore.path", "./data/malt.db")
	viper.SetDefault("kvstore.in_memory", true)
	viper.SetDefault("commitment.vector_size", 256)
	viper.SetDefault("cas.gateway_url", "https://ipfs.io/ipfs")
	viper.SetDefault("cas.timeout", "30s")

	// Enable environment variable binding
	viper.SetEnvPrefix("malt")
	viper.AutomaticEnv()

	// Support for config file
	viper.SetConfigName("malt")              // name of config file (without extension)
	viper.SetConfigType("json")              // REQUIRED if the config file does not have the extension
	viper.AddConfigPath(".")                 // optionally look for config in the working directory
	viper.AddConfigPath("$HOME/.malt")       // call multiple times to add multiple search paths
	viper.AddConfigPath("/etc/malt/")        // path to look for the config file in
}

// LoadConfig loads configuration from all sources.
// Returns the merged configuration.
func LoadConfig() (*Config, error) {
	// Try to read config file (ignore if not found)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			// Config file was found but another error was produced
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
		// Config file not found; ignore and use defaults
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unable to decode config: %w", err)
	}

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// LoadConfigFromFile loads configuration from a specific file.
func LoadConfigFromFile(path string) (*Config, error) {
	viper.SetConfigFile(path)

	var cfg Config
	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unable to decode config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
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

// DefaultConfig returns a config with default values.
// Useful for programmatic usage without viper.
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