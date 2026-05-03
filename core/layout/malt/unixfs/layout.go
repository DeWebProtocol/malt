// Package unixfs implements a MALT-native UnixFS-style layout directly on top
// of the map and list structural semantics.
//
// Directories and files are committed as map roots. Directory entries are map
// bindings from one path segment to a child map root. File payloads are stored
// under the reserved @payload binding; small payloads point to a raw CAS block,
// while large payloads point to a list root whose entries are chunk CIDs.
package unixfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	DefaultChunkSize = 256 * 1024

	typeFile      = "file"
	typeDirectory = "directory"

	typePrefix      = "malt:unixfs:type:v1:"
	sizePrefix      = "malt:unixfs:size:v1:"
	chunkSizePrefix = "malt:unixfs:chunk-size:v1:"
)

var (
	payloadPath   = arcset.CanonicalizePath("@payload")
	typePath      = arcset.CanonicalizePath("@type")
	sizePath      = arcset.CanonicalizePath("@size")
	chunkSizePath = arcset.CanonicalizePath("@chunksize")

	ErrNotFound     = errors.New("unixfs path not found")
	ErrNotDirectory = errors.New("unixfs path is not a directory")
	ErrNotFile      = errors.New("unixfs path is not a file")
	ErrReservedPath = errors.New("unixfs path uses a reserved segment")
	ErrInvalidPath  = errors.New("unixfs path contains an unsupported segment")
)

// Options configures a UnixFS layout instance.
type Options struct {
	Namespace string
	ChunkSize int
	Map       mapping.Semantics
	List      list.Semantics
	Blocks    cas.Client
}

// Layout materializes a UnixFS-style hierarchy with MALT map/list semantics.
type Layout struct {
	namespace string
	chunkSize int
	maps      mapping.Semantics
	lists     list.Semantics
	blocks    cas.Client
}

// Step records one verified map binding used during path resolution.
type Step struct {
	Root   cid.Cid
	Path   arcset.Path
	Target cid.Cid
	Proof  structure.Proof
}

// Resolution records terminal @payload materialization for a path.
type Resolution struct {
	NodeRoot cid.Cid
	Payload  cid.Cid
	Steps    []Step
}

// Stat describes a UnixFS layout node.
type Stat struct {
	Kind        string
	NodeRoot    cid.Cid
	Payload     cid.Cid
	StorageKind string
	Size        uint64
	ChunkSize   uint64
	Entries     []string
}

type fileInfo struct {
	nodeRoot  cid.Cid
	payload   cid.Cid
	size      uint64
	chunkSize uint64
}

// New creates a UnixFS layout over caller-supplied map/list semantics and CAS.
func New(opts Options) (*Layout, error) {
	if opts.Map == nil {
		return nil, fmt.Errorf("map semantic is nil")
	}
	if opts.List == nil {
		return nil, fmt.Errorf("list semantic is nil")
	}
	if opts.Blocks == nil {
		return nil, fmt.Errorf("CAS client is nil")
	}
	if opts.Namespace == "" {
		return nil, fmt.Errorf("namespace is empty")
	}

	chunkSize := opts.ChunkSize
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize < 0 {
		return nil, fmt.Errorf("chunk size must be positive")
	}

	return &Layout{
		namespace: opts.Namespace,
		chunkSize: chunkSize,
		maps:      opts.Map,
		lists:     opts.List,
		blocks:    opts.Blocks,
	}, nil
}

// EmptyDirectory commits an empty directory map root.
func (l *Layout) EmptyDirectory(ctx context.Context) (cid.Cid, error) {
	dirMarker, err := typeMarker(typeDirectory)
	if err != nil {
		return cid.Undef, err
	}
	payload, err := l.commitDirectoryManifest(ctx, nil)
	if err != nil {
		return cid.Undef, err
	}
	entries := map[arcset.Path]cid.Cid{
		typePath:    dirMarker,
		payloadPath: payload,
	}
	return l.maps.Commit(ctx, l.namespace, mapping.NewViewFromPaths(entries))
}

// AddDirectory ensures that path exists as a directory and returns the new root.
func (l *Layout) AddDirectory(ctx context.Context, root cid.Cid, path string) (cid.Cid, error) {
	if !root.Defined() {
		var err error
		root, err = l.EmptyDirectory(ctx)
		if err != nil {
			return cid.Undef, err
		}
	}

	segments, err := splitRelativePath(path)
	if err != nil {
		return cid.Undef, err
	}
	if len(segments) == 0 {
		return root, nil
	}
	return l.ensureDirectory(ctx, root, segments)
}

// AddFile writes data at path and returns the updated root directory.
func (l *Layout) AddFile(ctx context.Context, root cid.Cid, path string, data []byte) (cid.Cid, error) {
	if !root.Defined() {
		var err error
		root, err = l.EmptyDirectory(ctx)
		if err != nil {
			return cid.Undef, err
		}
	}

	segments, err := splitRelativePath(path)
	if err != nil {
		return cid.Undef, err
	}
	if len(segments) == 0 {
		return cid.Undef, fmt.Errorf("file path is empty")
	}
	return l.addFile(ctx, root, segments, data)
}

// Resolve traverses directory arcs and materializes the terminal @payload.
func (l *Layout) Resolve(ctx context.Context, root cid.Cid, path string) (*Resolution, error) {
	nodeRoot, steps, err := l.resolveNode(ctx, root, path)
	if err != nil {
		return nil, err
	}

	payload, proof, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @payload", ErrNotFound)
	}
	steps = append(steps, Step{
		Root:   nodeRoot,
		Path:   payloadPath,
		Target: payload,
		Proof:  proof,
	})

	return &Resolution{
		NodeRoot: nodeRoot,
		Payload:  payload,
		Steps:    steps,
	}, nil
}

// Stat resolves path and returns UnixFS node metadata.
func (l *Layout) Stat(ctx context.Context, root cid.Cid, path string) (*Stat, error) {
	nodeRoot, _, err := l.resolveNode(ctx, root, path)
	if err != nil {
		return nil, err
	}

	kind, err := l.nodeType(ctx, nodeRoot)
	if err != nil {
		return nil, err
	}
	payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @payload", ErrNotFound)
	}

	switch kind {
	case typeDirectory:
		entries, err := l.loadDirectoryManifest(ctx, payload)
		if err != nil {
			return nil, err
		}
		return &Stat{
			Kind:        typeDirectory,
			NodeRoot:    nodeRoot,
			Payload:     payload,
			StorageKind: "map",
			Entries:     entries,
		}, nil
	case typeFile:
		info, err := l.fileInfo(ctx, nodeRoot, payload)
		if err != nil {
			return nil, err
		}
		return &Stat{
			Kind:        typeFile,
			NodeRoot:    nodeRoot,
			Payload:     payload,
			StorageKind: storageKind(info.payload),
			Size:        info.size,
			ChunkSize:   info.chunkSize,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported unixfs node kind %q", kind)
	}
}

// ReadFile reads the complete file payload at path.
func (l *Layout) ReadFile(ctx context.Context, root cid.Cid, path string) ([]byte, error) {
	info, err := l.resolveFile(ctx, root, path)
	if err != nil {
		return nil, err
	}
	return l.readPayloadRange(ctx, info, 0, info.size)
}

// ReadFileRange reads a byte range from the file at path. Ranges past EOF are
// clipped; an offset at or beyond EOF returns an empty slice.
func (l *Layout) ReadFileRange(ctx context.Context, root cid.Cid, path string, offset, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}

	info, err := l.resolveFile(ctx, root, path)
	if err != nil {
		return nil, err
	}
	if offset >= info.size {
		return nil, nil
	}
	if length > info.size-offset {
		length = info.size - offset
	}
	return l.readPayloadRange(ctx, info, offset, length)
}

func (l *Layout) ensureDirectory(ctx context.Context, root cid.Cid, segments []string) (cid.Cid, error) {
	if len(segments) == 0 {
		return root, nil
	}

	key := arcset.CanonicalizePath(segments[0])
	child, _, ok, err := l.lookup(ctx, root, key)
	if err != nil {
		return cid.Undef, err
	}
	oldChild := cid.Undef
	if ok {
		oldChild = child
		kind, err := l.nodeType(ctx, child)
		if err != nil {
			return cid.Undef, err
		}
		if kind != typeDirectory {
			return cid.Undef, fmt.Errorf("%w: %s", ErrNotDirectory, segments[0])
		}
	} else {
		child, err = l.EmptyDirectory(ctx)
		if err != nil {
			return cid.Undef, err
		}
	}

	nextChild, err := l.ensureDirectory(ctx, child, segments[1:])
	if err != nil {
		return cid.Undef, err
	}
	return l.setChild(ctx, root, key, oldChild, nextChild)
}

func (l *Layout) addFile(ctx context.Context, root cid.Cid, segments []string, data []byte) (cid.Cid, error) {
	key := arcset.CanonicalizePath(segments[0])
	if len(segments) == 1 {
		oldChild, _, ok, err := l.lookup(ctx, root, key)
		if err != nil {
			return cid.Undef, err
		}
		if ok {
			kind, err := l.nodeType(ctx, oldChild)
			if err != nil {
				return cid.Undef, err
			}
			if kind == typeDirectory {
				return cid.Undef, fmt.Errorf("%w: %s", ErrNotFile, key.String())
			}
		}

		fileRoot, err := l.commitFile(ctx, data)
		if err != nil {
			return cid.Undef, err
		}
		return l.setChild(ctx, root, key, oldChild, fileRoot)
	}

	child, _, ok, err := l.lookup(ctx, root, key)
	if err != nil {
		return cid.Undef, err
	}
	oldChild := cid.Undef
	if ok {
		oldChild = child
		kind, err := l.nodeType(ctx, child)
		if err != nil {
			return cid.Undef, err
		}
		if kind != typeDirectory {
			return cid.Undef, fmt.Errorf("%w: %s", ErrNotDirectory, key.String())
		}
	} else {
		child, err = l.EmptyDirectory(ctx)
		if err != nil {
			return cid.Undef, err
		}
	}

	nextChild, err := l.addFile(ctx, child, segments[1:], data)
	if err != nil {
		return cid.Undef, err
	}
	return l.setChild(ctx, root, key, oldChild, nextChild)
}

func (l *Layout) commitFile(ctx context.Context, data []byte) (cid.Cid, error) {
	payload, err := l.commitPayload(ctx, data)
	if err != nil {
		return cid.Undef, err
	}
	fileMarker, err := typeMarker(typeFile)
	if err != nil {
		return cid.Undef, err
	}
	sizeMarker, err := uintMarker(sizePrefix, uint64(len(data)))
	if err != nil {
		return cid.Undef, err
	}
	chunkSizeMarker, err := uintMarker(chunkSizePrefix, uint64(l.chunkSize))
	if err != nil {
		return cid.Undef, err
	}

	entries := map[arcset.Path]cid.Cid{
		typePath:      fileMarker,
		payloadPath:   payload,
		sizePath:      sizeMarker,
		chunkSizePath: chunkSizeMarker,
	}
	return l.maps.Commit(ctx, l.namespace, mapping.NewViewFromPaths(entries))
}

func (l *Layout) commitPayload(ctx context.Context, data []byte) (cid.Cid, error) {
	if len(data) <= l.chunkSize {
		return l.blocks.Put(ctx, data)
	}

	chunks := make([]cid.Cid, 0, (len(data)+l.chunkSize-1)/l.chunkSize)
	for start := 0; start < len(data); start += l.chunkSize {
		end := start + l.chunkSize
		if end > len(data) {
			end = len(data)
		}
		chunkCID, err := l.blocks.Put(ctx, data[start:end])
		if err != nil {
			return cid.Undef, err
		}
		chunks = append(chunks, chunkCID)
	}
	return l.lists.Commit(ctx, l.namespace, list.NewViewFromSlice(chunks))
}

func (l *Layout) resolveNode(ctx context.Context, root cid.Cid, path string) (cid.Cid, []Step, error) {
	if !root.Defined() {
		return cid.Undef, nil, fmt.Errorf("root is undefined")
	}

	segments, err := splitRelativePath(path)
	if err != nil {
		return cid.Undef, nil, err
	}

	current := root
	steps := make([]Step, 0, len(segments)+1)
	for _, segment := range segments {
		key := arcset.CanonicalizePath(segment)
		target, proof, ok, err := l.lookup(ctx, current, key)
		if err != nil {
			return cid.Undef, nil, err
		}
		if !ok {
			return cid.Undef, nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		steps = append(steps, Step{
			Root:   current,
			Path:   key,
			Target: target,
			Proof:  proof,
		})
		current = target
	}
	return current, steps, nil
}

func (l *Layout) resolveFile(ctx context.Context, root cid.Cid, path string) (*fileInfo, error) {
	nodeRoot, _, err := l.resolveNode(ctx, root, path)
	if err != nil {
		return nil, err
	}

	kind, err := l.nodeType(ctx, nodeRoot)
	if err != nil {
		return nil, err
	}
	if kind != typeFile {
		return nil, fmt.Errorf("%w: %s", ErrNotFile, path)
	}

	payload, _, ok, err := l.lookup(ctx, nodeRoot, payloadPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @payload", ErrNotFound)
	}

	return l.fileInfo(ctx, nodeRoot, payload)
}

func (l *Layout) fileInfo(ctx context.Context, nodeRoot cid.Cid, payload cid.Cid) (*fileInfo, error) {
	sizeCID, _, ok, err := l.lookup(ctx, nodeRoot, sizePath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @size", ErrNotFound)
	}
	size, err := parseUintMarker(sizeCID, sizePrefix)
	if err != nil {
		return nil, err
	}

	chunkSizeCID, _, ok, err := l.lookup(ctx, nodeRoot, chunkSizePath)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: missing @chunksize", ErrNotFound)
	}
	chunkSize, err := parseUintMarker(chunkSizeCID, chunkSizePrefix)
	if err != nil {
		return nil, err
	}
	if chunkSize == 0 {
		return nil, fmt.Errorf("stored chunk size is zero")
	}

	return &fileInfo{
		nodeRoot:  nodeRoot,
		payload:   payload,
		size:      size,
		chunkSize: chunkSize,
	}, nil
}

func (l *Layout) readPayloadRange(ctx context.Context, info *fileInfo, offset, length uint64) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}

	if codec.SemanticKindOf(info.payload) == codec.SemanticKindList {
		return l.readListRange(ctx, info.payload, offset, length, info.chunkSize)
	}

	data, err := l.blocks.Get(ctx, info.payload)
	if err != nil {
		return nil, err
	}
	if offset > uint64(len(data)) {
		return nil, nil
	}
	end := offset + length
	if end > uint64(len(data)) {
		end = uint64(len(data))
	}
	return cloneBytes(data[offset:end]), nil
}

func (l *Layout) readListRange(ctx context.Context, root cid.Cid, offset, length, chunkSize uint64) ([]byte, error) {
	startIndex := offset / chunkSize
	endOffset := offset + length
	endIndex := (endOffset - 1) / chunkSize

	out := bytes.Buffer{}
	for index := startIndex; index <= endIndex; index++ {
		query, proof, err := l.lists.Prove(ctx, l.namespace, root, index)
		if err != nil {
			return nil, err
		}
		ok, err := l.lists.Verify(root, index, query, proof)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("list proof failed at index %d", index)
		}
		if !query.Key.Defined() {
			return nil, fmt.Errorf("%w: missing chunk %d", ErrNotFound, index)
		}

		chunk, err := l.blocks.Get(ctx, query.Key)
		if err != nil {
			return nil, err
		}

		chunkStart := index * chunkSize
		from := uint64(0)
		if offset > chunkStart {
			from = offset - chunkStart
		}
		to := uint64(len(chunk))
		if endOffset < chunkStart+to {
			to = endOffset - chunkStart
		}
		if from > to || to > uint64(len(chunk)) {
			return nil, fmt.Errorf("invalid chunk bounds at index %d", index)
		}
		out.Write(chunk[from:to])
	}
	return out.Bytes(), nil
}

func (l *Layout) nodeType(ctx context.Context, root cid.Cid) (string, error) {
	typeCID, _, ok, err := l.lookup(ctx, root, typePath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%w: missing @type", ErrNotFound)
	}
	return parseTypeMarker(typeCID)
}

func (l *Layout) lookup(ctx context.Context, root cid.Cid, key arcset.Path) (cid.Cid, structure.Proof, bool, error) {
	binding, proof, err := l.maps.Prove(ctx, l.namespace, root, key)
	if err != nil {
		if isMapAbsent(err) {
			return cid.Undef, nil, false, nil
		}
		return cid.Undef, nil, false, err
	}
	if !binding.Present || !binding.Value.Defined() {
		return cid.Undef, proof, false, nil
	}
	ok, err := l.maps.Verify(root, key, binding, proof)
	if err != nil {
		return cid.Undef, nil, false, err
	}
	if !ok {
		return cid.Undef, nil, false, fmt.Errorf("map proof failed for %s", key.String())
	}
	return binding.Value, proof, true, nil
}

func (l *Layout) set(ctx context.Context, root cid.Cid, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	if key.IsEmpty() {
		return cid.Undef, fmt.Errorf("key is empty")
	}
	if !newValue.Defined() {
		return cid.Undef, fmt.Errorf("new value is undefined")
	}
	if !oldValue.Defined() {
		oldValue = cid.Undef
	}
	return l.maps.Update(ctx, l.namespace, root, key, oldValue, newValue)
}

func (l *Layout) setChild(ctx context.Context, root cid.Cid, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	nextRoot, err := l.set(ctx, root, key, oldValue, newValue)
	if err != nil {
		return cid.Undef, err
	}
	return l.addDirectoryEntry(ctx, nextRoot, key.String())
}

func (l *Layout) addDirectoryEntry(ctx context.Context, root cid.Cid, name string) (cid.Cid, error) {
	payload, _, ok, err := l.lookup(ctx, root, payloadPath)
	if err != nil {
		return cid.Undef, err
	}
	if !ok {
		return cid.Undef, fmt.Errorf("%w: missing directory @payload", ErrNotFound)
	}
	entries, err := l.loadDirectoryManifest(ctx, payload)
	if err != nil {
		return cid.Undef, err
	}
	if slices.Contains(entries, name) {
		return root, nil
	}
	entries = append(entries, name)
	nextPayload, err := l.commitDirectoryManifest(ctx, entries)
	if err != nil {
		return cid.Undef, err
	}
	return l.set(ctx, root, payloadPath, payload, nextPayload)
}

func (l *Layout) commitDirectoryManifest(ctx context.Context, entries []string) (cid.Cid, error) {
	m := manifest.Normalize(&manifest.DirectoryManifest{Entries: entries})
	payload, err := m.MarshalJSON()
	if err != nil {
		return cid.Undef, err
	}
	return l.blocks.Put(ctx, payload)
}

func (l *Layout) loadDirectoryManifest(ctx context.Context, payload cid.Cid) ([]string, error) {
	data, err := l.blocks.Get(ctx, payload)
	if err != nil {
		return nil, err
	}
	m, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		return nil, err
	}
	return slices.Clone(m.Entries), nil
}

func splitRelativePath(path string) ([]string, error) {
	canonical := arcset.CanonicalizePath(path)
	if canonical.IsEmpty() {
		return nil, nil
	}
	segments := canonical.Segments()
	for _, segment := range segments {
		if strings.HasPrefix(segment, "@") {
			return nil, fmt.Errorf("%w: %s", ErrReservedPath, segment)
		}
		if !isPortableUnixFSSegment(segment) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPath, segment)
		}
	}
	return segments, nil
}

func isPortableUnixFSSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func isMapAbsent(err error) bool {
	return strings.Contains(err.Error(), "not found")
}

func typeMarker(kind string) (cid.Cid, error) {
	return identityCID([]byte(typePrefix + kind))
}

func parseTypeMarker(c cid.Cid) (string, error) {
	payload, err := identityPayload(c)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(payload), typePrefix) {
		return "", fmt.Errorf("invalid unixfs type marker")
	}
	kind := strings.TrimPrefix(string(payload), typePrefix)
	switch kind {
	case typeFile, typeDirectory:
		return kind, nil
	default:
		return "", fmt.Errorf("unknown unixfs type %q", kind)
	}
}

func uintMarker(prefix string, value uint64) (cid.Cid, error) {
	return identityCID([]byte(prefix + strconv.FormatUint(value, 10)))
}

func parseUintMarker(c cid.Cid, prefix string) (uint64, error) {
	payload, err := identityPayload(c)
	if err != nil {
		return 0, err
	}
	text := string(payload)
	if !strings.HasPrefix(text, prefix) {
		return 0, fmt.Errorf("invalid uint marker")
	}
	return strconv.ParseUint(strings.TrimPrefix(text, prefix), 10, 64)
}

func identityCID(payload []byte) (cid.Cid, error) {
	if len(payload) > math.MaxInt32 {
		return cid.Undef, fmt.Errorf("identity payload too large")
	}
	hash, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, hash), nil
}

func identityPayload(c cid.Cid) ([]byte, error) {
	decoded, err := mh.Decode(c.Hash())
	if err != nil {
		return nil, err
	}
	if decoded.Code != mh.IDENTITY {
		return nil, fmt.Errorf("CID is not an identity marker")
	}
	return decoded.Digest, nil
}

func cloneBytes(data []byte) []byte {
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func storageKind(c cid.Cid) string {
	switch codec.SemanticKindOf(c) {
	case codec.SemanticKindList:
		return "list"
	case codec.SemanticKindMap:
		return "map"
	default:
		return "raw"
	}
}
