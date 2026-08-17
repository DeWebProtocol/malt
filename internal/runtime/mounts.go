package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dewebprotocol/malt-client/cache"
	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
	truststore "github.com/dewebprotocol/malt-client/trust"
	"github.com/dewebprotocol/malt-client/unixfs"
	clientverifier "github.com/dewebprotocol/malt-core/sdk/verifier"
	cid "github.com/ipfs/go-cid"
)

var (
	ErrPlatformMountUnavailable  = errors.New("platform mount adapter is unavailable")
	ErrEncryptedMountUnavailable = errors.New("encrypted filesystem mount is unavailable")
)

type trustStateReader interface {
	GetState(string) (truststore.RootState, error)
}

type acceptedViewSelector struct {
	trust        trustStateReader
	remoteSource string
}

func (s acceptedViewSelector) SelectView(ctx context.Context, spec filesystemmount.Spec) (filesystemservice.View, error) {
	if err := ctx.Err(); err != nil {
		return filesystemservice.View{}, err
	}
	if spec.EncryptionEpoch != 0 {
		return filesystemservice.View{}, fmt.Errorf("%w: epoch %d requires a local decryption layer", ErrEncryptedMountUnavailable, spec.EncryptionEpoch)
	}
	if s.trust == nil {
		return filesystemservice.View{}, fmt.Errorf("accepted-view trust store is nil")
	}
	state, err := s.trust.GetState(spec.TrustAlias)
	if err != nil {
		return filesystemservice.View{}, err
	}
	if state.Profile != "unixfs" {
		return filesystemservice.View{}, fmt.Errorf("trusted root alias %q has profile %q, want unixfs", spec.TrustAlias, state.Profile)
	}
	if state.Accepted == nil {
		return filesystemservice.View{}, truststore.ErrNoAcceptedRoot
	}
	root, err := cid.Parse(state.Accepted.Root)
	if err != nil {
		return filesystemservice.View{}, fmt.Errorf("accepted root for %q is invalid: %w", spec.TrustAlias, err)
	}
	var revision uint64
	for _, observation := range state.ObservedHeads {
		if observation.DatasetID != spec.DatasetID || observation.Branch != spec.Branch ||
			strings.TrimRight(observation.Source, "/") != strings.TrimRight(s.remoteSource, "/") {
			continue
		}
		observed, parseErr := cid.Parse(observation.Root)
		if parseErr == nil && observed.Equals(root) && observation.Revision > revision {
			revision = observation.Revision
		}
	}
	return filesystemservice.View{
		DatasetID: spec.DatasetID, Branch: spec.Branch, Root: root,
		Revision: revision, EncryptionEpoch: spec.EncryptionEpoch,
	}, nil
}

type datasetBranch struct {
	dataset string
	branch  string
}

type viewFilesystemFactory func(string, string) (filesystemmount.ViewFilesystem, error)
type writableBindingFactory func(context.Context, filesystemmount.Spec, filesystemservice.View, filesystemmount.ViewFilesystem) (filesystemmount.WritableBinding, error)

// gatewayFilesystemRouter keeps concrete Gateway HTTP types inside the runtime
// composition root. The mount manager and platform adapter see only semantic
// filesystem capabilities and immutable Views.
type gatewayFilesystemRouter struct {
	mu       sync.Mutex
	open     viewFilesystemFactory
	bind     writableBindingFactory
	services map[datasetBranch]filesystemmount.ViewFilesystem
}

func newGatewayFilesystemRouter(open viewFilesystemFactory, bind writableBindingFactory) (*gatewayFilesystemRouter, error) {
	if open == nil {
		return nil, fmt.Errorf("Gateway filesystem factory is nil")
	}
	return &gatewayFilesystemRouter{open: open, bind: bind, services: map[datasetBranch]filesystemmount.ViewFilesystem{}}, nil
}

func (r *gatewayFilesystemRouter) filesystem(view filesystemservice.View) (filesystemmount.ViewFilesystem, error) {
	if r == nil || r.open == nil {
		return nil, fmt.Errorf("Gateway filesystem router is nil")
	}
	key := datasetBranch{dataset: strings.TrimSpace(view.DatasetID), branch: strings.TrimSpace(view.Branch)}
	if key.dataset == "" || key.branch == "" {
		return nil, fmt.Errorf("filesystem View dataset and branch are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if service := r.services[key]; !nilInterface(service) {
		return service, nil
	}
	service, err := r.open(key.dataset, key.branch)
	if err != nil {
		return nil, err
	}
	if nilInterface(service) {
		return nil, fmt.Errorf("Gateway filesystem factory returned nil")
	}
	r.services[key] = service
	return service, nil
}

func (r *gatewayFilesystemRouter) Stat(ctx context.Context, view filesystemservice.View, path string) (filesystemservice.Info, error) {
	service, err := r.filesystem(view)
	if err != nil {
		return filesystemservice.Info{}, err
	}
	return service.Stat(ctx, view, path)
}

func (r *gatewayFilesystemRouter) ReadDir(ctx context.Context, view filesystemservice.View, path string) ([]filesystemservice.DirEntry, error) {
	service, err := r.filesystem(view)
	if err != nil {
		return nil, err
	}
	return service.ReadDir(ctx, view, path)
}

func (r *gatewayFilesystemRouter) Open(ctx context.Context, view filesystemservice.View, path string) (*filesystemservice.Handle, error) {
	service, err := r.filesystem(view)
	if err != nil {
		return nil, err
	}
	return service.Open(ctx, view, path)
}

func (r *gatewayFilesystemRouter) BindWritable(ctx context.Context, spec filesystemmount.Spec, view filesystemservice.View) (filesystemmount.WritableBinding, error) {
	if r == nil || r.bind == nil {
		return nil, fmt.Errorf("Gateway filesystem router has no write-back binding factory")
	}
	canonical, err := filesystemmount.NormalizeSpec(spec)
	if err != nil {
		return nil, err
	}
	if canonical.WritePolicy != filesystemmount.WriteBack || canonical.DatasetID != view.DatasetID || canonical.Branch != view.Branch {
		return nil, fmt.Errorf("write-back mount Spec does not match its selected View")
	}
	service, err := r.filesystem(view)
	if err != nil {
		return nil, err
	}
	binding, err := r.bind(ctx, canonical, view, service)
	if nilInterface(binding) {
		binding = nil
	}
	if err != nil {
		return binding, err
	}
	if binding == nil {
		return nil, fmt.Errorf("Gateway write-back binding factory returned nil")
	}
	return binding, nil
}

// NewMountManager composes the locally authoritative selector, verified
// Gateway reader, non-authoritative cache, durable registry, and platform
// adapter. It never observes or accepts a remote head.
func NewMountManager(cfg *clientconfig.Config) (*filesystemmount.Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtime config is nil")
	}
	adapter, err := newPlatformMountAdapter()
	if err != nil {
		return nil, err
	}
	registry, err := filesystemmount.OpenStore(cfg.Filesystem.MountsPath)
	if err != nil {
		return nil, fmt.Errorf("open mount registry: %w", err)
	}
	payloadCache, err := cache.Open(cfg.Filesystem.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("open filesystem cache: %w", err)
	}
	trust, err := truststore.Open(cfg.Daemon.StatePath)
	if err != nil {
		return nil, fmt.Errorf("open filesystem trust store: %w", err)
	}
	verifier, err := clientverifier.NewDefault()
	if err != nil {
		return nil, fmt.Errorf("initialize filesystem verifier: %w", err)
	}
	writerFactory := &clientRootWriterFactory{}
	router, err := newGatewayFilesystemRouter(func(datasetID, branch string) (filesystemmount.ViewFilesystem, error) {
		options, err := requiredGatewayOptions(cfg, datasetID, branch)
		if err != nil {
			return nil, err
		}
		remote, err := gatewayclient.New(options)
		if err != nil {
			return nil, err
		}
		reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote, Verifier: verifier})
		if err != nil {
			return nil, err
		}
		return filesystemservice.New(filesystemservice.Options{
			Reader: reader, Cache: payloadCache, Verifier: verifier,
		})
	}, func(ctx context.Context, spec filesystemmount.Spec, view filesystemservice.View, service filesystemmount.ViewFilesystem) (filesystemmount.WritableBinding, error) {
		options, err := requiredGatewayOptions(cfg, spec.DatasetID, spec.Branch)
		if err != nil {
			return nil, err
		}
		remote, err := gatewayclient.New(options)
		if err != nil {
			return nil, err
		}
		return newGatewayWritableBinding(ctx, gatewayWritableBindingOptions{
			Spec: spec, View: view, Base: viewFilesystemBase{filesystem: service},
			Remote: remote, Roots: trust, WriterFactory: writerFactory,
			StateDirectory:     cfg.Filesystem.WritableStateDir,
			MaxStagedFileBytes: cfg.Filesystem.MaxStagedFileBytes,
			Source:             "filesystem mount " + spec.ID + " via Gateway",
		})
	})
	if err != nil {
		return nil, err
	}
	return filesystemmount.NewManager(filesystemmount.Options{
		Store: registry,
		Selector: acceptedViewSelector{
			trust: trust, remoteSource: cfg.GatewayBaseURL(),
		},
		Filesystem: router,
		Adapter:    adapter,
	})
}

var (
	_ filesystemmount.ViewSelector           = acceptedViewSelector{}
	_ filesystemmount.ViewFilesystem         = (*gatewayFilesystemRouter)(nil)
	_ filesystemmount.WritableViewFilesystem = (*gatewayFilesystemRouter)(nil)
)
