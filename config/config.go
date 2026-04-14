// Package config provides configuration file parsing.
// It only handles loading from file/env/flags, not component configuration.
package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"
)

// Config holds raw configuration values loaded from file/env/flags.
// Components use their own Options pattern for configuration.
type Config struct {
	CommitmentType string                `mapstructure:"commitment_type"`
	KVStoreType    string                `mapstructure:"kvstore_type"`
	EATType        string                `mapstructure:"eat_type"`
	CASType        string                `mapstructure:"cas_type"`
	KVStore        KVStoreConfig         `mapstructure:"kvstore"`
	Commitment     CommitmentConfig      `mapstructure:"commitment"`
	CAS            CASConfig             `mapstructure:"cas"`
	Logging        LoggingConfig         `mapstructure:"logging"`
}

type KVStoreConfig struct {
	Path     string `mapstructure:"path"`
	InMemory bool   `mapstructure:"in_memory"`
}

type CommitmentConfig struct {
	VectorSize int `mapstructure:"vector_size"`
}

type CASConfig struct {
	GatewayURL  string `mapstructure:"gateway_url"`
	Timeout     string `mapstructure:"timeout"`
	MockLatency string `mapstructure:"mock_latency"` // mock CAS uniform latency (e.g. "100ms")
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Init initializes Viper with default values.
func Init() {
	viper.SetDefault("commitment_type", "kzg")
	viper.SetDefault("kvstore_type", "memory")
	viper.SetDefault("eat_type", "versioned")
	viper.SetDefault("cas_type", "mock")
	viper.SetDefault("kvstore.path", "./data/malt.db")
	viper.SetDefault("kvstore.in_memory", true)
	viper.SetDefault("commitment.vector_size", 256)
	viper.SetDefault("cas.gateway_url", "https://ipfs.io/ipfs")
	viper.SetDefault("cas.timeout", "30s")
	viper.SetDefault("logging.level", "info")
	viper.SetDefault("logging.format", "json")

	viper.SetEnvPrefix("malt")
	viper.AutomaticEnv()

	viper.SetConfigName("malt")
	viper.SetConfigType("json")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.malt")
	viper.AddConfigPath("/etc/malt/")
}

// Load loads configuration from all sources.
func Load() (*Config, error) {
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unable to decode config: %w", err)
	}

	return &cfg, nil
}

// LoadFromFile loads configuration from a specific file.
func LoadFromFile(path string) (*Config, error) {
	viper.SetConfigFile(path)

	var cfg Config
	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unable to decode config: %w", err)
	}

	return &cfg, nil
}

// CASTimeout parses and returns the CAS timeout.
func (c *Config) CASTimeout() (time.Duration, error) {
	return time.ParseDuration(c.CAS.Timeout)
}

// CASMockLatency parses and returns the mock CAS latency.
// Returns 0 if not set, letting the mock use its internal defaults.
func (c *Config) CASMockLatency() (time.Duration, error) {
	if c.CAS.MockLatency == "" {
		return 0, nil
	}
	return time.ParseDuration(c.CAS.MockLatency)
}

// String returns a string representation of the config.
func (c *Config) String() string {
	return fmt.Sprintf("Config{commitment=%s, kv=%s, eat=%s, cas=%s}",
		c.CommitmentType, c.KVStoreType, c.EATType, c.CASType)
}
