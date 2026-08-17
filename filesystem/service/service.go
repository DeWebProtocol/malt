// Package service projects a caller-selected MALT dataset root as a
// platform-neutral, read-only filesystem. It contains no mount-driver or
// transport-specific code.
package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dewebprotocol/malt-client/cache"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

var ErrClosed = errors.New("filesystem handle is closed")

// View fixes every identity that can affect a mounted read. Root must be a
// locally selected accepted root; callers must not construct a View directly
// from an observed remote head.
type View struct {
	DatasetID       string
	Branch          string
	Root            cid.Cid
	Revision        uint64
	EncryptionEpoch uint32
}

// Info is transport- and platform-neutral filesystem metadata.
type Info struct {
	Path        string
	Name        string
	Kind        string
	NodeRoot    cid.Cid
	Payload     cid.Cid
	StorageKind string
	Size        uint64
}

func (i Info) IsDir() bool { return i.Kind == unixfs.StagedKindDirectory }

// DirEntry is one verified immediate child of a directory.
type DirEntry struct {
	Name string
	Kind string
}

type Options struct {
	Reader   unixfs.Reader
	Cache    *cache.Store
	Verifier unixfs.LocalVerifier
	Now      func() time.Time
}

type Service struct {
	reader   unixfs.LookupReader
	cache    *cache.Store
	verifier unixfs.LocalVerifier
	now      func() time.Time
}

func New(opts Options) (*Service, error) {
	if opts.Reader == nil {
		return nil, fmt.Errorf("filesystem UnixFS reader is nil")
	}
	reader, ok := opts.Reader.(unixfs.LookupReader)
	if !ok {
		return nil, fmt.Errorf("filesystem UnixFS reader lacks verified lookup capability")
	}
	if opts.Cache != nil && opts.Verifier == nil {
		return nil, fmt.Errorf("filesystem cache requires a local MALT verifier")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{reader: reader, cache: opts.Cache, verifier: opts.Verifier, now: now}, nil
}

// Stat resolves a path below the exact caller-selected root and returns only
// locally verified UnixFS projection metadata.
func (s *Service) Stat(ctx context.Context, view View, rawPath string) (Info, error) {
	view, err := normalizeView(view)
	if err != nil {
		return Info{}, err
	}
	canonical, segments, err := canonicalPath(rawPath)
	if err != nil {
		return Info{}, err
	}
	stat, err := s.reader.Lookup(ctx, view.Root, canonical)
	if err != nil {
		return Info{}, err
	}
	if err := validateStat(view, segments, stat); err != nil {
		return Info{}, err
	}
	if stat.Kind == unixfs.StagedKindDirectory {
		return infoFromStat(canonical, stat), nil
	}
	if stat.PayloadKind == "raw" && s.cache != nil {
		body, err := s.readRaw(ctx, view, canonical, stat)
		if err != nil {
			return Info{}, err
		}
		stat.Size = uint64(len(body))
		return infoFromStat(canonical, stat), nil
	}
	stat, err = s.reader.Stat(ctx, view.Root, canonical)
	if err != nil {
		return Info{}, err
	}
	if err := validateStat(view, segments, stat); err != nil {
		return Info{}, err
	}
	return infoFromStat(canonical, stat), nil
}

// ReadDir returns the authenticated immediate children of a directory.
func (s *Service) ReadDir(ctx context.Context, view View, rawPath string) ([]DirEntry, error) {
	view, err := normalizeView(view)
	if err != nil {
		return nil, err
	}
	canonical, segments, err := canonicalPath(rawPath)
	if err != nil {
		return nil, err
	}
	stat, err := s.reader.Lookup(ctx, view.Root, canonical)
	if err != nil {
		return nil, err
	}
	if err := validateStat(view, segments, stat); err != nil {
		return nil, err
	}
	if stat.Kind != unixfs.StagedKindDirectory {
		return nil, unixfs.ErrNotDirectory
	}
	entries := make([]DirEntry, len(stat.Entries))
	for index, entry := range stat.Entries {
		kind, err := projectionKind(entry.Type)
		if errors.Is(err, errUnknownProjectionKind) {
			childPath := path.Join(canonical, entry.Name)
			childSegments := append(append([]string(nil), segments...), entry.Name)
			child, statErr := s.reader.Lookup(ctx, view.Root, childPath)
			if statErr != nil {
				return nil, statErr
			}
			if statErr := validateStat(view, childSegments, child); statErr != nil {
				return nil, statErr
			}
			kind = child.Kind
			err = nil
		}
		if err != nil {
			return nil, err
		}
		entries[index] = DirEntry{Name: entry.Name, Kind: kind}
	}
	slices.SortFunc(entries, func(left, right DirEntry) int { return strings.Compare(left.Name, right.Name) })
	return entries, nil
}

// Open verifies that rawPath is a file and returns a handle pinned to view.
func (s *Service) Open(ctx context.Context, view View, rawPath string) (*Handle, error) {
	view, err := normalizeView(view)
	if err != nil {
		return nil, err
	}
	canonical, _, err := canonicalPath(rawPath)
	if err != nil {
		return nil, err
	}
	info, err := s.Stat(ctx, view, canonical)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, unixfs.ErrNotFile
	}
	return &Handle{service: s, view: view, path: canonical, info: info}, nil
}

// ReadFile returns a complete locally verified file body.
func (s *Service) ReadFile(ctx context.Context, view View, rawPath string) ([]byte, Info, error) {
	return s.read(ctx, view, rawPath, 0, nil)
}

// ReadFileRange returns at most length bytes beginning at offset. Large List
// payloads remain lazy: only the authenticated range is fetched.
func (s *Service) ReadFileRange(ctx context.Context, view View, rawPath string, offset, length uint64) ([]byte, Info, error) {
	return s.read(ctx, view, rawPath, offset, &length)
}

func (s *Service) read(ctx context.Context, view View, rawPath string, offset uint64, length *uint64) ([]byte, Info, error) {
	view, err := normalizeView(view)
	if err != nil {
		return nil, Info{}, err
	}
	canonical, segments, err := canonicalPath(rawPath)
	if err != nil {
		return nil, Info{}, err
	}
	stat, err := s.reader.Lookup(ctx, view.Root, canonical)
	if err != nil {
		return nil, Info{}, err
	}
	if err := validateStat(view, segments, stat); err != nil {
		return nil, Info{}, err
	}
	if stat.Kind != unixfs.StagedKindFile {
		return nil, infoFromStat(canonical, stat), unixfs.ErrNotFile
	}

	if stat.PayloadKind == "raw" {
		body, err := s.readRaw(ctx, view, canonical, stat)
		if err != nil {
			return nil, infoFromStat(canonical, stat), err
		}
		stat.Size = uint64(len(body))
		info := infoFromStat(canonical, stat)
		return sliceRange(body, offset, length), info, nil
	}
	stat, err = s.reader.Stat(ctx, view.Root, canonical)
	if err != nil {
		return nil, Info{}, err
	}
	if err := validateStat(view, segments, stat); err != nil {
		return nil, Info{}, err
	}
	info := infoFromStat(canonical, stat)
	if length != nil && (*length == 0 || offset >= stat.Size) {
		return []byte{}, info, nil
	}

	var result *unixfs.ReadResult
	if length == nil {
		result, err = s.reader.ReadFile(ctx, view.Root, canonical)
	} else {
		result, err = s.reader.ReadFileRange(ctx, view.Root, canonical, offset, *length)
	}
	if err != nil {
		return nil, info, err
	}
	if err := validateReadResult(stat, offset, length, result); err != nil {
		return nil, info, err
	}
	return append([]byte(nil), result.Body...), info, nil
}

func (s *Service) readRaw(ctx context.Context, view View, canonical string, stat *unixfs.Stat) ([]byte, error) {
	binding := cache.Binding{
		DatasetID: view.DatasetID, Branch: view.Branch, Root: view.Root,
		Revision: view.Revision, CID: stat.Payload, EncryptionEpoch: view.EncryptionEpoch,
	}
	if s.cache != nil {
		verifier, err := newCacheProofVerifier(s.verifier, view, canonical, stat)
		if err != nil {
			return nil, err
		}
		body, _, cacheErr := s.cache.ReadVerified(ctx, binding, verifier)
		if cacheErr == nil {
			return body, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}

	result, err := s.reader.ReadFile(ctx, view.Root, canonical)
	if err != nil {
		return nil, err
	}
	if err := validateRawReadResult(stat, result); err != nil {
		return nil, err
	}
	body := append([]byte(nil), result.Body...)
	computed, err := stat.Payload.Prefix().Sum(body)
	if err != nil || !computed.Equals(stat.Payload) {
		return nil, fmt.Errorf("verified UnixFS raw body does not match its authenticated CID")
	}
	if s.cache != nil {
		evidence, err := marshalCacheEvidence(view, canonical, stat)
		if err != nil {
			return nil, err
		}
		_, _ = s.cache.PutVerified(binding, body, cache.VerificationEvidence{
			Profile: cacheEvidenceProfile, Evidence: evidence, VerifiedAt: s.now().UTC(),
		})
	}
	return body, nil
}

// Handle is an immutable selected-root file handle. Reads revalidate the path
// projection and never advance the selected view to an observed remote head.
type Handle struct {
	mu      sync.Mutex
	service *Service
	view    View
	path    string
	info    Info
	closed  bool
}

func (h *Handle) Info() Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.info
}

func (h *Handle) Read(ctx context.Context, offset, length uint64) ([]byte, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	service, view, filePath := h.service, h.view, h.path
	h.mu.Unlock()
	body, info, err := service.ReadFileRange(ctx, view, filePath, offset, length)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if !h.closed {
		h.info = info
	}
	h.mu.Unlock()
	return body, nil
}

func (h *Handle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return nil
}

func normalizeView(view View) (View, error) {
	view.DatasetID = strings.TrimSpace(view.DatasetID)
	view.Branch = strings.TrimSpace(view.Branch)
	if view.DatasetID == "" || view.Branch == "" || !utf8.ValidString(view.DatasetID) || !utf8.ValidString(view.Branch) {
		return View{}, fmt.Errorf("filesystem dataset and branch are required valid UTF-8")
	}
	if !view.Root.Defined() {
		return View{}, fmt.Errorf("filesystem selected root is undefined")
	}
	return view, nil
}

func canonicalPath(raw string) (string, []string, error) {
	segments, err := unixfsmodel.ParsePath(raw)
	if err != nil {
		return "", nil, err
	}
	return strings.Join(segments, "/"), segments, nil
}

func validateStat(view View, segments []string, stat *unixfs.Stat) error {
	if stat == nil {
		return fmt.Errorf("verified UnixFS reader returned a nil stat")
	}
	if stat.Kind != unixfs.StagedKindFile && stat.Kind != unixfs.StagedKindDirectory {
		return fmt.Errorf("verified UnixFS reader returned unsupported kind %q", stat.Kind)
	}
	if !stat.NodeRoot.Defined() || !stat.Payload.Defined() || stat.Resolution.Target != stat.NodeRoot {
		return fmt.Errorf("verified UnixFS stat has inconsistent node or payload identity")
	}
	if err := validateResolution(view.Root, segments, stat.Resolution, stat.NodeRoot); err != nil {
		return err
	}
	if stat.PayloadBinding != nil {
		payloadSegments := append(append([]string(nil), segments...), "@payload")
		if err := validateResolution(view.Root, payloadSegments, *stat.PayloadBinding, stat.Payload); err != nil {
			return err
		}
	} else if !stat.Payload.Equals(stat.NodeRoot) {
		return fmt.Errorf("verified UnixFS stat has an unbound payload target")
	}
	switch stat.Kind {
	case unixfs.StagedKindFile:
		if stat.PayloadKind != "raw" && stat.PayloadKind != "list" {
			return fmt.Errorf("verified UnixFS file has unsupported payload kind %q", stat.PayloadKind)
		}
		if len(stat.Entries) != 0 {
			return fmt.Errorf("verified UnixFS file contains directory entries")
		}
	case unixfs.StagedKindDirectory:
		if stat.Size != 0 {
			return fmt.Errorf("verified UnixFS directory declares a file size")
		}
	}
	return nil
}

func validateResolution(root cid.Cid, segments []string, resolution unixfs.Resolution, target cid.Cid) error {
	if resolution.Request.Profile != protocol.ResolveProfile || resolution.Result.Profile != protocol.ResolveProfile ||
		resolution.Request.Root != root.String() || !slices.Equal(resolution.Request.Segments, segments) ||
		!resolution.Target.Equals(target) || resolution.Result.Target != target.String() {
		return fmt.Errorf("verified UnixFS resolution does not match the selected root and path")
	}
	return nil
}

func validateReadResult(stat *unixfs.Stat, offset uint64, length *uint64, result *unixfs.ReadResult) error {
	if result == nil || !result.Target.Equals(stat.Payload) || result.TotalSize != stat.Size || result.Offset != offset {
		return fmt.Errorf("verified UnixFS read does not match the requested file")
	}
	expectedEnd := stat.Size
	if length != nil {
		expectedEnd = saturatingAdd(offset, *length)
		if expectedEnd > stat.Size {
			expectedEnd = stat.Size
		}
	}
	if result.End != expectedEnd || uint64(len(result.Body)) != expectedEnd-offset {
		return fmt.Errorf("verified UnixFS read returned an inconsistent range")
	}
	return nil
}

func validateRawReadResult(stat *unixfs.Stat, result *unixfs.ReadResult) error {
	if result == nil || !result.Target.Equals(stat.Payload) || result.Offset != 0 ||
		result.End != result.TotalSize || result.TotalSize != uint64(len(result.Body)) {
		return fmt.Errorf("verified UnixFS raw read does not match the requested file")
	}
	return nil
}

func infoFromStat(canonical string, stat *unixfs.Stat) Info {
	name := ""
	if canonical != "" {
		name = path.Base(canonical)
	}
	return Info{
		Path: canonical, Name: name, Kind: stat.Kind, NodeRoot: stat.NodeRoot,
		Payload: stat.Payload, StorageKind: stat.PayloadKind, Size: stat.Size,
	}
}

var errUnknownProjectionKind = errors.New("directory entry kind requires legacy projection")

func projectionKind(value unixfsmodel.DirectoryEntryType) (string, error) {
	switch value {
	case unixfsmodel.DirectoryEntryTypeDir:
		return unixfs.StagedKindDirectory, nil
	case unixfsmodel.DirectoryEntryTypeFile:
		return unixfs.StagedKindFile, nil
	case unixfsmodel.DirectoryEntryTypeUnknown:
		return "", errUnknownProjectionKind
	default:
		return "", fmt.Errorf("directory contains unresolved entry kind %q", value)
	}
}

func sliceRange(body []byte, offset uint64, length *uint64) []byte {
	if offset >= uint64(len(body)) || (length != nil && *length == 0) {
		return []byte{}
	}
	end := uint64(len(body))
	if length != nil {
		end = saturatingAdd(offset, *length)
		if end > uint64(len(body)) {
			end = uint64(len(body))
		}
	}
	return append([]byte(nil), body[offset:end]...)
}

func saturatingAdd(left, right uint64) uint64 {
	if ^uint64(0)-left < right {
		return ^uint64(0)
	}
	return left + right
}
