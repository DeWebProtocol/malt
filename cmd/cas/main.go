// Command cas starts a standalone mock CAS HTTP server.
//
// Usage:
//
//	cas [flags]          Run in foreground
//	cas init             Create ~/.malt/cas/settings.json
//	cas start            Start the CAS daemon in the background
//	cas status           Show the CAS daemon status
//	cas stop             Stop the managed CAS daemon
//	cas restart          Restart the managed CAS daemon
//
// Examples:
//
//	# Initialize settings
//	cas init
//
//	# Start as background daemon
//	cas start
//
//	# No latency (fast local testing)
//	cas --no-latency
//
//	# IPFS-like latency
//	cas --get-latency 2100ms --put-latency 1400ms --has-latency 100ms
//
//	# Custom port
//	cas --listen 127.0.0.1:9999 --get-latency 50ms
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	"github.com/dewebprotocol/malt/storage/kv"
	"github.com/dewebprotocol/malt/storage/kv/badger"
	kvfs "github.com/dewebprotocol/malt/storage/kv/fs"
	"github.com/spf13/cobra"
)

var (
	configFile string
	listen     string
	getLatency time.Duration
	putLatency time.Duration
	hasLatency time.Duration
	jitter     time.Duration
	noLatency  bool
)

var rootCmd = &cobra.Command{
	Use:   "cas",
	Short: "Standalone mock CAS HTTP server for MALT",
	Args:  cobra.NoArgs,
	RunE:  run,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "settings file path (default ~/.malt/cas/settings.json)")
	rootCmd.PersistentFlags().StringVar(&listen, "listen", "", "listen address (overrides settings)")
	rootCmd.PersistentFlags().DurationVar(&getLatency, "get-latency", 0, "simulated Get latency (0 = default IPFS latency)")
	rootCmd.PersistentFlags().DurationVar(&putLatency, "put-latency", 0, "simulated Put latency (0 = default IPFS latency)")
	rootCmd.PersistentFlags().DurationVar(&hasLatency, "has-latency", 0, "simulated Has latency (0 = default IPFS latency)")
	rootCmd.PersistentFlags().DurationVar(&jitter, "jitter", 0, "latency jitter (±)")
	rootCmd.PersistentFlags().BoolVar(&noLatency, "no-latency", false, "disable all latency simulation")
}

func main() {
	if os.Getenv(daemonProcessKey) == "1" {
		if err := runDaemonChild(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runDaemonChild is the entry point for the forked daemon child process.
func runDaemonChild() error {
	configPath := os.Getenv(daemonConfigKey)
	cfg, err := Load(configPath)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if listenOverride := os.Getenv(daemonListenKey); listenOverride != "" {
		cfg.Listen = listenOverride
	}
	if err := applyDaemonEnvOverrides(); err != nil {
		return err
	}
	return runServer(cfg)
}

func run(cmd *cobra.Command, args []string) error {
	cfg, err := Load(configFile)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if listen != "" {
		cfg.Listen = listen
	}
	return runServer(cfg)
}

func daemonOverridesFromGlobals() DaemonOverrides {
	return DaemonOverrides{
		Listen:     listen,
		NoLatency:  noLatency,
		GetLatency: getLatency,
		PutLatency: putLatency,
		HasLatency: hasLatency,
		Jitter:     jitter,
	}
}

func applyDaemonEnvOverrides() error {
	if os.Getenv(daemonNoLatencyKey) == "1" {
		noLatency = true
	}
	var err error
	if getLatency, err = durationFromEnv(daemonGetLatencyKey, getLatency); err != nil {
		return err
	}
	if putLatency, err = durationFromEnv(daemonPutLatencyKey, putLatency); err != nil {
		return err
	}
	if hasLatency, err = durationFromEnv(daemonHasLatencyKey, hasLatency); err != nil {
		return err
	}
	if jitter, err = durationFromEnv(daemonJitterKey, jitter); err != nil {
		return err
	}
	return nil
}

func durationFromEnv(key string, current time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return current, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return d, nil
}

// runServer creates the KV store, starts the CAS HTTP server, and blocks
// until interrupted. Used by both foreground mode and daemon child.
func runServer(cfg *Config) error {
	kvStore, kvClose, err := newKVStore(cfg)
	if err != nil {
		return fmt.Errorf("init kvstore: %w", err)
	}
	if kvClose != nil {
		defer kvClose()
	}

	var opts []casmock.Option
	if kvStore != nil {
		opts = append(opts, casmock.WithKVStore(kvStore))
	}

	if noLatency {
		opts = append(opts, casmock.WithoutLatency())
	} else {
		if getLatency > 0 {
			opts = append(opts, casmock.WithGetLatency(getLatency))
		}
		if putLatency > 0 {
			opts = append(opts, casmock.WithPutLatency(putLatency))
		}
		if hasLatency > 0 {
			opts = append(opts, casmock.WithHasLatency(hasLatency))
		}
		if jitter > 0 {
			opts = append(opts, casmock.WithJitter(jitter))
		}
	}

	store := casmock.NewCAS(opts...)
	srv := casmock.NewHTTPServer(cfg.Listen, store)
	shutdownCh := make(chan struct{}, 1)
	handler := daemonShutdownHandler(srv.Handler(), os.Getenv(daemonShutdownTokenKey), shutdownCh)

	fmt.Fprintf(os.Stderr, "mock-cas listening on %s\n", cfg.Listen)
	fmt.Fprintf(os.Stderr, "kvstore: type=%s\n", cfg.KVStore.Type)
	if cfg.KVStore.Type != "memory" {
		kvPath, err := cfg.ResolveKVStorePath()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "data dir: %s\n", kvPath)
	}
	if noLatency {
		fmt.Fprintf(os.Stderr, "latency: disabled\n")
	} else if getLatency > 0 || putLatency > 0 || hasLatency > 0 {
		fmt.Fprintf(os.Stderr, "latency: get=%s put=%s has=%s jitter=%s\n",
			getLatency, putLatency, hasLatency, jitter)
	} else {
		fmt.Fprintf(os.Stderr, "latency: get=%s put=%s has=%s jitter=%s (IPFS defaults)\n",
			casmock.DefaultGetLatency, casmock.DefaultPutLatency,
			casmock.DefaultHasLatency, casmock.DefaultJitter)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	errCh := make(chan error, 1)
	httpSrv := &http.Server{Addr: cfg.Listen, Handler: handler}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "\nshutting down...\n")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		return httpSrv.Shutdown(shutCtx)
	case <-shutdownCh:
		fmt.Fprintf(os.Stderr, "\nshutdown requested...\n")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		return httpSrv.Shutdown(shutCtx)
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}

func daemonShutdownHandler(next http.Handler, token string, shutdownCh chan<- struct{}) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shutdown" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-MALT-CAS-Shutdown-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		select {
		case shutdownCh <- struct{}{}:
		default:
		}
	})
}

// newKVStore creates a KV store from config. Returns (store, closeFn, error).
// For memory mode, returns (nil, nil, nil) — casmock defaults to memory.
func newKVStore(cfg *Config) (kvstore.KVStore, func() error, error) {
	switch cfg.KVStore.Type {
	case "memory":
		return nil, nil, nil
	case "badger":
		kvPath, err := cfg.ResolveKVStorePath()
		if err != nil {
			return nil, nil, err
		}
		kv, err := badger.New(
			badger.WithPath(kvPath),
			badger.WithInMemory(false),
		)
		if err != nil {
			return nil, nil, err
		}
		return kv, kv.Close, nil
	case "fs":
		kvPath, err := cfg.ResolveKVStorePath()
		if err != nil {
			return nil, nil, err
		}
		kv, err := kvfs.New(kvPath)
		if err != nil {
			return nil, nil, err
		}
		return kv, kv.Close, nil
	default:
		return nil, nil, fmt.Errorf("unsupported kvstore type: %s", cfg.KVStore.Type)
	}
}
