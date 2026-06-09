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
//	# Custom port
//	cas --listen 127.0.0.1:9999
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
	return DaemonOverrides{Listen: listen}
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
