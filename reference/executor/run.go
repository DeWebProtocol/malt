// Package executor hosts the all-in-one reference MALT execution backend. It
// owns ArcTable/KV materialization, proof generation, optional UnixFS content
// adaptation, and CAS connectivity. It is not a client daemon and must not be
// treated as the verifier's trust boundary.
package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/runtime/node"
	"github.com/dewebprotocol/malt/server"
)

// Options configures reference executor startup.
type Options struct {
	ListenOverride string
	APILabel       string
	LifecycleToken string
	Stdout         io.Writer
	Stderr         io.Writer
}

// Handle is a running reference executor instance that can be shut down.
type Handle struct {
	srv    *server.Server
	node   *node.Node
	Listen string
}

// Shutdown gracefully stops the reference executor.
func (h *Handle) Shutdown(ctx context.Context) error {
	if h.srv != nil {
		if err := h.srv.Shutdown(ctx); err != nil {
			return err
		}
	}
	h.node.Close()
	return nil
}

// Start launches the reference executor in the background and returns a handle.
// Unlike Run, Start does not block — the caller is responsible for calling
// Shutdown when done.
func Start(cfg *config.Config, opts Options) (*Handle, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	effective := *cfg
	if opts.ListenOverride != "" {
		effective.RPC.Listen = opts.ListenOverride
	}

	n, err := node.NewNode(node.WithConfig(&effective))
	if err != nil {
		return nil, err
	}

	srv := server.New(
		n,
		effective.RPC.Listen,
		server.WithLifecycleToken(opts.LifecycleToken),
		server.WithBrowserOrigins(effective.RPC.CORSAllowedOrigins),
	)
	go func() {
		_ = srv.Start()
	}()

	return &Handle{
		srv:    srv,
		node:   n,
		Listen: effective.RPC.Listen,
	}, nil
}

// Run starts the reference-executor HTTP API, then blocks until shutdown or a fatal
// server error occurs.
func Run(cfg *config.Config, opts Options) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	effective := *cfg
	if opts.ListenOverride != "" {
		effective.RPC.Listen = opts.ListenOverride
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	label := opts.APILabel
	if label == "" {
		label = "MALT reference executor"
	}

	n, err := node.NewNode(node.WithConfig(&effective))
	if err != nil {
		return err
	}
	defer n.Close()

	srv := server.New(
		n,
		effective.RPC.Listen,
		server.WithLifecycleToken(opts.LifecycleToken),
		server.WithBrowserOrigins(effective.RPC.CORSAllowedOrigins),
	)
	srvErr := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
	}()

	fmt.Fprintf(stdout, "%s listening on %s\n", label, effective.RPC.Listen)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-stop:
		fmt.Fprintf(stderr, "received signal %s, shutting down\n", sig)
	case err := <-srvErr:
		return fmt.Errorf("reference executor server failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	return nil
}
