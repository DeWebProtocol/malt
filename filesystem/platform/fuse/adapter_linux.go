//go:build linux

// Package fusefs is the outermost Linux FUSE adapter for the MALT local
// runtime. It translates kernel operations into a read-only filesystem that is
// already pinned to a locally selected immutable View.
package fusefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

var (
	ErrForeignMount     = errors.New("mountpoint is occupied by a foreign filesystem")
	ErrUnsafeMountpoint = errors.New("mountpoint is not a safe empty directory")
	ErrMountIdentity    = errors.New("mounted filesystem identity does not match the requested MALT mount")
)

type server interface {
	Unmount() error
	Wait()
}

type mountFunc func(string, fs.InodeEmbedder, *fs.Options) (server, error)
type inspectFunc func(string) (*mountIdentity, error)
type recoverFunc func(context.Context, string, mountIdentity) error

type Adapter struct {
	mount   mountFunc
	inspect inspectFunc
	recover recoverFunc
}

func New() *Adapter {
	return &Adapter{
		mount: func(mountpoint string, root fs.InodeEmbedder, options *fs.Options) (server, error) {
			return fs.Mount(mountpoint, root, options)
		},
		inspect: readMountIdentity,
		recover: recoverFuseMount,
	}
}

func (*Adapter) Name() string { return "linux-fuse" }

func (a *Adapter) Mount(ctx context.Context, spec filesystemmount.Spec, filesystem filesystemmount.ReadOnlyFilesystem) (filesystemmount.Session, error) {
	if a == nil || a.mount == nil || a.inspect == nil || a.recover == nil {
		return nil, fmt.Errorf("FUSE adapter is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filesystem == nil {
		return nil, fmt.Errorf("FUSE filesystem capability is nil")
	}
	if strings.TrimSpace(spec.ID) == "" || spec.ID != strings.TrimSpace(spec.ID) {
		return nil, fmt.Errorf("FUSE mount ID is invalid")
	}
	mountpoint, err := safeMountpoint(spec.Mountpoint)
	if err != nil {
		return nil, err
	}
	identity, err := a.inspect(mountpoint)
	if err != nil {
		return nil, err
	}
	if identity != nil {
		if !ownedMount(identity, spec) {
			return nil, fmt.Errorf("%w: %s is %s from %s", ErrForeignMount, mountpoint, identity.Filesystem, identity.Source)
		}
		if err := a.recover(ctx, mountpoint, *identity); err != nil {
			return nil, fmt.Errorf("recover stale MALT FUSE mount: %w", err)
		}
		if remaining, err := a.inspect(mountpoint); err != nil {
			return nil, err
		} else if remaining != nil {
			return nil, fmt.Errorf("stale MALT FUSE mount remains at %s", mountpoint)
		}
	}
	if err := requireEmptyDirectory(mountpoint); err != nil {
		return nil, err
	}
	root := newRoot(filesystem)
	options := &fs.Options{
		MountOptions: fuse.MountOptions{
			FsName: expectedSource(spec), Name: "malt", Options: []string{"ro"},
			DisableXAttrs: true,
		},
		NullPermissions: true,
		RootStableAttr:  &fs.StableAttr{Mode: fuse.S_IFDIR},
	}
	mounted, err := a.mount(mountpoint, root, options)
	if err != nil {
		return nil, err
	}
	if mounted == nil {
		return nil, fmt.Errorf("FUSE mount returned a nil server")
	}
	cleanup := func(primary error) error {
		if unmountErr := mounted.Unmount(); unmountErr != nil {
			return errors.Join(primary, fmt.Errorf("cleanup FUSE mount: %w", unmountErr))
		}
		return primary
	}
	if err := ctx.Err(); err != nil {
		return nil, cleanup(err)
	}
	identity, err = a.inspect(mountpoint)
	if err != nil {
		return nil, cleanup(err)
	}
	if identity == nil || !ownedMount(identity, spec) {
		return nil, cleanup(fmt.Errorf("%w at %s", ErrMountIdentity, mountpoint))
	}
	return newSession(mounted), nil
}

func (a *Adapter) RecoverUnmount(ctx context.Context, spec filesystemmount.Spec) error {
	if a == nil || a.inspect == nil || a.recover == nil {
		return fmt.Errorf("FUSE adapter is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	mountpoint, err := canonicalMountpoint(spec.Mountpoint)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	identity, err := a.inspect(mountpoint)
	if err != nil {
		return err
	}
	if identity == nil {
		return nil
	}
	if !ownedMount(identity, spec) {
		return fmt.Errorf("%w: refusing to unmount %s from %s", ErrForeignMount, identity.Filesystem, identity.Source)
	}
	if err := a.recover(ctx, mountpoint, *identity); err != nil {
		return err
	}
	remaining, err := a.inspect(mountpoint)
	if err != nil {
		return err
	}
	if remaining != nil {
		return fmt.Errorf("FUSE mount remains at %s after recovery", mountpoint)
	}
	return nil
}

func expectedSource(spec filesystemmount.Spec) string { return "malt:" + spec.ID }

func ownedMount(identity *mountIdentity, spec filesystemmount.Spec) bool {
	if identity == nil || identity.Source != expectedSource(spec) {
		return false
	}
	return identity.Filesystem == "fuse.malt" || identity.Filesystem == "fuse"
}

func safeMountpoint(raw string) (string, error) {
	mountpoint, err := canonicalMountpoint(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnsafeMountpoint, err)
	}
	return mountpoint, nil
}

func canonicalMountpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !filepath.IsAbs(raw) {
		return "", fmt.Errorf("mountpoint must be absolute")
	}
	clean := filepath.Clean(raw)
	if filepath.Dir(clean) == clean {
		return "", fmt.Errorf("mountpoint must not be a filesystem root")
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("mountpoint must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	clean, err = filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) != filepath.Clean(clean) {
		return "", fmt.Errorf("mountpoint path must not traverse symlinks")
	}
	return filepath.Clean(clean), nil
}

func requireEmptyDirectory(mountpoint string) error {
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeMountpoint, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: mountpoint is not empty", ErrUnsafeMountpoint)
	}
	return nil
}

func recoverFuseMount(ctx context.Context, mountpoint string, expected mountIdentity) error {
	current, err := readMountIdentity(mountpoint)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if *current != expected {
		return fmt.Errorf("%w: mount identity changed before recovery", ErrForeignMount)
	}
	binary, err := exec.LookPath("fusermount3")
	if err != nil {
		binary, err = exec.LookPath("fusermount")
	}
	if err != nil {
		return fmt.Errorf("find fusermount helper: %w", err)
	}
	var stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "-u", mountpoint)
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			return err
		}
		return fmt.Errorf("%s: %w", message, err)
	}
	return nil
}

type session struct {
	server         server
	done           chan error
	mu             sync.Mutex
	unmountStarted bool
	unmountDone    chan struct{}
	unmountErr     error
}

func newSession(server server) *session {
	s := &session{server: server, done: make(chan error, 1)}
	go func() {
		server.Wait()
		s.done <- nil
		close(s.done)
	}()
	return s
}

func (s *session) Done() <-chan error { return s.done }

func (s *session) Unmount(ctx context.Context) error {
	s.mu.Lock()
	if !s.unmountStarted {
		s.unmountStarted = true
		s.unmountDone = make(chan struct{})
		go func() {
			err := s.server.Unmount()
			s.mu.Lock()
			s.unmountErr = err
			close(s.unmountDone)
			s.mu.Unlock()
		}()
	}
	done := s.unmountDone
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.unmountErr
	}
}

var (
	_ filesystemmount.Adapter = (*Adapter)(nil)
	_ filesystemmount.Session = (*session)(nil)
)
