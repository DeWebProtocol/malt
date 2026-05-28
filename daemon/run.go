// Package daemon hosts the shared local daemon bootstrap used by CLI entrypoints.
package daemon

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
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

// RunOptions configures daemon process startup.
type RunOptions struct {
	ListenOverride string
	APILabel       string
	LifecycleToken string
	Stdout         io.Writer
	Stderr         io.Writer
}

// Run starts the daemon HTTP API and optional embedded mock CAS, then blocks
// until shutdown or a fatal server error occurs.
func Run(cfg *config.Config, opts RunOptions) error {
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
		label = "malt daemon"
	}

	var (
		nodeOpts    []node.Option
		mockSrv     *casmock.HTTPServer
		mockSrvErr  chan error
		mockCASInst *casmock.CAS
	)

	if effective.CAS.Mode == "embedded-mock" {
		mockOpts, err := embeddedMockCASOptions(&effective)
		if err != nil {
			return err
		}
		mockCASInst = casmock.NewCAS(mockOpts...)
		nodeOpts = append(nodeOpts, node.WithCAS(mockCASInst))
		mockSrv = casmock.NewHTTPServer(effective.CAS.EmbeddedMock.Listen, mockCASInst)
		mockSrvErr = make(chan error, 1)
		go func() {
			if err := mockSrv.Start(); err != nil && err != http.ErrServerClosed {
				mockSrvErr <- err
			}
		}()
	}

	nodeOpts = append(nodeOpts, node.WithConfig(&effective))
	node, err := node.NewNode(nodeOpts...)
	if err != nil {
		return err
	}
	defer node.Close()

	srv := server.New(
		node,
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
	if effective.CAS.Mode == "embedded-mock" {
		fmt.Fprintf(stdout, "embedded mock CAS listening on %s\n", effective.CAS.EmbeddedMock.Listen)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-stop:
		fmt.Fprintf(stderr, "received signal %s, shutting down\n", sig)
	case err := <-srvErr:
		return fmt.Errorf("daemon server failed: %w", err)
	case err := <-mockServerError(mockSrvErr):
		return fmt.Errorf("embedded mock CAS failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if mockSrv != nil {
		_ = mockSrv.Shutdown(ctx)
	}
	if err := srv.Shutdown(ctx); err != nil {
		return err
	}
	return nil
}

func mockServerError(ch chan error) <-chan error {
	if ch == nil {
		empty := make(chan error)
		return empty
	}
	return ch
}

func embeddedMockCASOptions(cfg *config.Config) ([]casmock.Option, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	latency, err := cfg.EmbeddedMockLatency()
	if err != nil {
		return nil, fmt.Errorf("invalid embedded mock latency: %w", err)
	}
	if latency <= 0 {
		return []casmock.Option{casmock.WithoutLatency()}, nil
	}
	return []casmock.Option{
		casmock.WithGetLatency(latency),
		casmock.WithPutLatency(latency),
		casmock.WithHasLatency(latency),
	}, nil
}
