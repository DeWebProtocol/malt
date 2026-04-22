package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dewebprotocol/malt/core/api"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/server"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(daemonCmd)
	daemonCmd.Flags().String("listen", "", "override daemon listen address")
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the local MALT daemon",
	RunE:  runDaemon,
}

func runDaemon(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}

	if override, _ := cmd.Flags().GetString("listen"); override != "" {
		cfg.RPC.Listen = override
	}

	var (
		nodeOpts    []api.Option
		mockSrv     *casmock.HTTPServer
		mockSrvErr  chan error
		mockCASInst *casmock.CAS
	)

	if cfg.CAS.Mode == "embedded-mock" {
		mockOpts := []casmock.Option{}
		if latency, err := cfg.EmbeddedMockLatency(); err == nil && latency > 0 {
			mockOpts = append(mockOpts,
				casmock.WithGetLatency(latency),
				casmock.WithPutLatency(latency),
				casmock.WithHasLatency(latency),
			)
		}
		mockCASInst = casmock.NewCAS(mockOpts...)
		nodeOpts = append(nodeOpts, api.WithCAS(mockCASInst))
		mockSrv = casmock.NewHTTPServer(cfg.CAS.EmbeddedMock.Listen, mockCASInst)
		mockSrvErr = make(chan error, 1)
		go func() {
			if err := mockSrv.Start(); err != nil && err != http.ErrServerClosed {
				mockSrvErr <- err
			}
		}()
	}

	nodeOpts = append(nodeOpts, api.WithConfig(cfg))
	node, err := api.NewNode(nodeOpts...)
	if err != nil {
		return err
	}
	defer node.Close()

	srv := server.New(node, cfg.RPC.Listen)
	srvErr := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
	}()

	fmt.Fprintf(os.Stdout, "malt daemon listening on %s\n", cfg.RPC.Listen)
	if cfg.CAS.Mode == "embedded-mock" {
		fmt.Fprintf(os.Stdout, "embedded mock CAS listening on %s\n", cfg.CAS.EmbeddedMock.Listen)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case sig := <-stop:
		fmt.Fprintf(os.Stderr, "received signal %s, shutting down\n", sig)
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
