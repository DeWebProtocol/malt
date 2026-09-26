package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/dewebprotocol/malt-client/application"
	"github.com/dewebprotocol/malt-client/bucketsync"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/trust"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

// DatasetLayout binds all content operations to the same explicit application
// layout. A managed Bucket owns its layout; an override must agree with it.
func DatasetLayout(ctx context.Context, remote *transport.Client, requested string) (unixfs.LayoutKind, error) {
	if remote == nil {
		return "", fmt.Errorf("content transport is nil")
	}
	var override unixfs.LayoutKind
	if requested != "" {
		var err error
		override, err = unixfs.ParseLayoutKind(requested)
		if err != nil {
			return "", err
		}
	}
	if remote.SelectedBucket() == "" {
		if override != "" {
			return override, nil
		}
		return unixfs.LayoutHybridV1, nil
	}
	bucket, err := remote.GetBucket(ctx)
	if err != nil {
		return "", err
	}
	layout, err := unixfs.ParseLayoutKind(string(bucket.Layout))
	if err != nil {
		return "", fmt.Errorf("decode selected Bucket layout: %w", err)
	}
	if override != "" && override != layout {
		return "", fmt.Errorf("--layout conflicts with selected Bucket layout %s", layout)
	}
	return layout, nil
}

// Content owns one configuration snapshot and the resources used by verified
// native content commands. It never accepts a root on behalf of its caller.
type Content struct {
	*application.UnixFS
	config *clientconfig.Config
	remote *transport.Client
	roots  *application.Roots
	blocks *CASBinding
	layout unixfs.LayoutKind
}

func (s *Services) OpenContent(ctx context.Context, selector, requestedLayout string) (_ *Content, resultErr error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	opts, err := GatewayOptions(cfg, cfg.Gateway.Bucket, "")
	if err != nil {
		return nil, err
	}
	remote, err := transport.New(opts)
	if err != nil {
		return nil, err
	}
	layout, err := DatasetLayout(ctx, remote, requestedLayout)
	if err != nil {
		return nil, err
	}
	roots := application.NewExplicitRootSelector()
	if _, err := roots.Select(selector); err != nil {
		store, err := trust.Open(cfg.Daemon.StatePath)
		if err != nil {
			return nil, fmt.Errorf("open trust store: %w", err)
		}
		roots, err = application.NewRoots(store)
		if err != nil {
			return nil, err
		}
	}
	blocks, err := ComposeCAS(cfg, remote, true)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, blocks.Close())
		}
	}()
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: blocks, Layout: layout})
	if err != nil {
		return nil, err
	}
	app, err := application.NewUnixFS(reader, nil, roots)
	if err != nil {
		return nil, err
	}
	return &Content{UnixFS: app, config: cfg, remote: remote, roots: roots, blocks: blocks, layout: layout}, nil
}

func (c *Content) Close() error { return c.blocks.Close() }

// RemovePath captures the exact selected base before materialization and stages
// the resulting candidate against that same base, even if local trust advances.
func (c *Content) RemovePath(ctx context.Context, selector, path string) (*unixfs.RemoveResult, error) {
	selected, err := c.roots.Select(selector)
	if err != nil {
		return nil, err
	}
	var syncer *bucketsync.Service
	var base bucketsync.Head
	if c.remote.SelectedBucket() != "" {
		syncer, err = bucketsync.OpenRemote(c.config.Workspace.StatePath, c.remote, c.remote.SelectedBucket())
		if err != nil {
			return nil, err
		}
		base, err = syncer.CurrentBase(selected.Root)
		if err != nil {
			return nil, err
		}
	}
	layout, err := unixfs.NewLayout(c.layout)
	if err != nil {
		return nil, err
	}
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: c.remote, Blocks: c.blocks, Layout: layout})
	if err != nil {
		return nil, err
	}
	app, err := application.NewUnixFS(writer, writer, application.NewExplicitRootSelector())
	if err != nil {
		return nil, err
	}
	result, err := app.RemovePath(ctx, selected.Root.String(), path)
	if err != nil {
		return nil, err
	}
	if selected.Alias != "" {
		if _, err := c.roots.RecordCandidate(selected.Alias, result.CandidateRoot, selected.Root, "unixfs remove"); err != nil {
			return nil, fmt.Errorf("record removal candidate: %w", err)
		}
	}
	if syncer != nil {
		if _, err := syncer.Stage(result.CandidateRoot, base, cid.Undef, "malt rm"); err != nil {
			return nil, fmt.Errorf("candidate %s was materialized but could not be staged: %w", result.CandidateRoot, err)
		}
	}
	return result, nil
}
