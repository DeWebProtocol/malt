package encrypted

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
)

type GraphWriter interface {
	unixfs.StagedRootCreator
	unixfs.FixedListPayloadWriter
}

type BlockWriter interface {
	unixfs.StagedBlockStore
}

type builderOptions struct {
	Graph              GraphWriter
	Blocks             BlockWriter
	PlaintextChunkSize int
}

type builder struct {
	graph     GraphWriter
	blocks    BlockWriter
	chunkSize int
}

type BindingSource struct {
	DatasetID   string
	DatasetName string
	Branch      string
	BindingID   string
	BindingName string
	PathName    string
	Source      string
	Root        *os.Root
	Epoch       uint32
	BucketKey   [32]byte
	IndexKey    [32]byte
}

type PreparedBinding struct {
	Manifest        BindingManifest `json:"manifest"`
	Root            cid.Cid         `json:"root"`
	Epoch           uint32          `json:"key_epoch"`
	Files           int             `json:"files"`
	Directories     int             `json:"directories"`
	EncryptedBytes  int64           `json:"encrypted_bytes"`
	ImmutableBlocks int             `json:"immutable_blocks"`
	MALTObjects     int             `json:"malt_objects"`
	// SourceFingerprint binds the canonical source metadata and the exact
	// plaintext bytes consumed by this local encryption pass. It is a local
	// consistency signal and must never be serialized to an untrusted peer.
	SourceFingerprint string `json:"-"`
}

type DatasetBuildRequest struct {
	DatasetID     string
	PlanID        string
	DatasetName   string
	Branch        string
	Epoch         uint32
	BucketKey     [32]byte
	IndexKey      [32]byte
	Bindings      []PreparedBinding
	ReuseManifest *DatasetView
}

type DatasetBuildResult struct {
	Root            cid.Cid
	ManifestCID     cid.Cid
	EncryptedBytes  int64
	ImmutableBlocks int
	MALTObjects     int
}

type buildStats struct {
	files           int
	directories     int
	encryptedBytes  int64
	immutableBlocks int
	maltObjects     int
	sourceName      string
	sourceHash      hash.Hash
}

func newBuilder(opts builderOptions) (*builder, error) {
	if opts.Graph == nil || opts.Blocks == nil {
		return nil, fmt.Errorf("encrypted UnixFS graph and block writers are required")
	}
	chunkSize := opts.PlaintextChunkSize
	if chunkSize == 0 {
		chunkSize = defaultPlaintextChunkSize
	}
	if chunkSize <= 0 || chunkSize > int(^uint32(0))-envelopeOverhead {
		return nil, fmt.Errorf("encrypted UnixFS plaintext chunk size is invalid")
	}
	return &builder{graph: opts.Graph, blocks: opts.Blocks, chunkSize: chunkSize}, nil
}

// PrepareBinding snapshots one local directory into an independently
// authenticated encrypted subtree using the caller-supplied local graph and
// block capabilities. Product composition uses Snapshot so this operation
// neither publishes nor accepts a dataset root.
func (b *builder) PrepareBinding(ctx context.Context, request BindingSource) (PreparedBinding, error) {
	if b == nil {
		return PreparedBinding{}, fmt.Errorf("encrypted UnixFS builder is nil")
	}
	if strings.TrimSpace(request.DatasetID) == "" || strings.TrimSpace(request.DatasetName) == "" ||
		strings.TrimSpace(request.Branch) == "" || strings.TrimSpace(request.BindingID) == "" ||
		strings.TrimSpace(request.BindingName) == "" || request.PathName == "" ||
		request.Source == "" || request.Epoch == 0 ||
		!utf8.ValidString(request.DatasetID) || !utf8.ValidString(request.DatasetName) ||
		!utf8.ValidString(request.Branch) || !utf8.ValidString(request.BindingID) ||
		!utf8.ValidString(request.BindingName) || !utf8.ValidString(request.PathName) {
		return PreparedBinding{}, fmt.Errorf("encrypted UnixFS binding source is incomplete")
	}
	if err := validateEntryName(request.PathName); err != nil {
		return PreparedBinding{}, fmt.Errorf("encrypted UnixFS binding path name: %w", err)
	}
	sourceRoot := request.Root
	closeRoot := false
	var info fs.FileInfo
	if sourceRoot == nil {
		absSource, err := filepath.Abs(request.Source)
		if err != nil {
			return PreparedBinding{}, fmt.Errorf("resolve encrypted UnixFS source: %w", err)
		}
		info, err = os.Lstat(absSource)
		if err != nil {
			return PreparedBinding{}, fmt.Errorf("stat encrypted UnixFS source: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return PreparedBinding{}, fmt.Errorf("encrypted UnixFS binding source must be a real directory")
		}
		sourceRoot, err = os.OpenRoot(absSource)
		if err != nil {
			return PreparedBinding{}, fmt.Errorf("open encrypted UnixFS source root: %w", err)
		}
		closeRoot = true
		defer sourceRoot.Close()
	}
	opened, err := sourceRoot.Open(".")
	if err != nil {
		return PreparedBinding{}, fmt.Errorf("pin encrypted UnixFS source root: %w", err)
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil {
		return PreparedBinding{}, errors.Join(statErr, closeErr)
	}
	if !openedInfo.IsDir() || (closeRoot && !os.SameFile(info, openedInfo)) {
		return PreparedBinding{}, fmt.Errorf("encrypted UnixFS binding source changed while it was opened")
	}
	absSource, err := filepath.Abs(request.Source)
	if err != nil {
		return PreparedBinding{}, fmt.Errorf("resolve encrypted UnixFS source fingerprint name: %w", err)
	}
	key := bindingKey(request.BucketKey, request.DatasetID, request.Branch, request.BindingID)
	stats := &buildStats{sourceName: filepath.Base(absSource), sourceHash: sha256.New()}
	root, err := b.buildDirectory(ctx, directoryBuildContext{
		datasetID: request.DatasetID, branch: request.Branch, bindingID: request.BindingID,
		root: sourceRoot, relativePath: "", epoch: request.Epoch, key: key,
		indexKey: bindingKey(request.IndexKey, request.DatasetID, request.Branch, request.BindingID),
	}, stats)
	if err != nil {
		return PreparedBinding{}, err
	}
	return PreparedBinding{
		Manifest: BindingManifest{
			ID: request.BindingID, Name: request.BindingName, PathName: request.PathName,
			Token: bindingToken(request.IndexKey, request.DatasetID, request.Branch, request.BindingID),
		},
		Root: root, Epoch: request.Epoch, Files: stats.files, Directories: stats.directories,
		EncryptedBytes: stats.encryptedBytes, ImmutableBlocks: stats.immutableBlocks, MALTObjects: stats.maltObjects,
		SourceFingerprint: "sha256:" + hex.EncodeToString(stats.sourceHash.Sum(nil)),
	}, nil
}

// BuildDataset authenticates the encrypted root manifest and each opaque
// binding token. ReuseManifestCID must come from a locally verified base root;
// it keeps independent binding-only changes from racing on @payload.
func (b *builder) BuildDataset(ctx context.Context, request DatasetBuildRequest) (DatasetBuildResult, error) {
	if b == nil {
		return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS builder is nil")
	}
	if strings.TrimSpace(request.DatasetID) == "" || strings.TrimSpace(request.PlanID) == "" || strings.TrimSpace(request.DatasetName) == "" ||
		strings.TrimSpace(request.Branch) == "" || request.Epoch == 0 || len(request.Bindings) == 0 {
		return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS dataset build request is incomplete")
	}
	manifests := make([]BindingManifest, len(request.Bindings))
	bindings := make(map[string]string, len(request.Bindings)+1)
	seenIDs := make(map[string]struct{}, len(request.Bindings))
	for index, binding := range request.Bindings {
		if !binding.Root.Defined() {
			return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS binding %q has no root", binding.Manifest.ID)
		}
		expectedToken := bindingToken(request.IndexKey, request.DatasetID, request.Branch, binding.Manifest.ID)
		if binding.Manifest.Token != expectedToken {
			return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS binding %q token does not match this dataset", binding.Manifest.ID)
		}
		if _, ok := seenIDs[binding.Manifest.ID]; ok {
			return DatasetBuildResult{}, fmt.Errorf("duplicate encrypted UnixFS binding ID %q", binding.Manifest.ID)
		}
		seenIDs[binding.Manifest.ID] = struct{}{}
		manifests[index] = binding.Manifest
		bindings[binding.Manifest.Token] = binding.Root.String()
	}
	manifests = canonicalDatasetBindings(manifests)
	manifest := DatasetManifest{
		Profile: ProfileID, Version: ProfileVersion, DatasetID: request.DatasetID, PlanID: request.PlanID,
		DatasetName: request.DatasetName, Branch: request.Branch, Bindings: manifests,
	}
	if err := validateDatasetManifest(manifest); err != nil {
		return DatasetBuildResult{}, err
	}
	manifestCID := cid.Undef
	result := DatasetBuildResult{}
	if request.ReuseManifest != nil {
		verified, err := request.ReuseManifest.verifiedSnapshot()
		if err != nil {
			return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS reused manifest is not a locally verified dataset view: %w", err)
		}
		expected, err := encodeCanonical(manifest)
		if err != nil {
			return DatasetBuildResult{}, err
		}
		observed, err := encodeCanonical(verified.manifest)
		if err != nil {
			return DatasetBuildResult{}, err
		}
		if !bytes.Equal(expected, observed) {
			return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS reused manifest does not match the requested dataset metadata")
		}
		manifestCID = verified.manifestCID
	}
	if !manifestCID.Defined() {
		plaintext, err := encodeCanonical(manifest)
		if err != nil {
			return DatasetBuildResult{}, err
		}
		key := datasetManifestKey(request.BucketKey, request.DatasetID, request.Branch)
		sealed, err := sealEnvelope(kindDatasetManifest, request.Epoch, key, plaintext, request.DatasetID, request.Branch)
		if err != nil {
			return DatasetBuildResult{}, err
		}
		manifestCID, err = b.putVerified(ctx, sealed)
		if err != nil {
			return DatasetBuildResult{}, fmt.Errorf("store encrypted UnixFS dataset manifest: %w", err)
		}
		result.EncryptedBytes += int64(len(sealed))
		result.ImmutableBlocks++
	}
	bindings["@payload"] = manifestCID.String()
	root, err := b.graph.CreateStagedRoot(ctx, bindings)
	if err != nil {
		return DatasetBuildResult{}, fmt.Errorf("materialize encrypted UnixFS dataset root: %w", err)
	}
	if !root.Defined() {
		return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS dataset root is undefined")
	}
	result.Root = root
	result.ManifestCID = manifestCID
	result.MALTObjects++
	return result, nil
}

type directoryBuildContext struct {
	datasetID    string
	branch       string
	bindingID    string
	root         *os.Root
	relativePath string
	epoch        uint32
	key          [32]byte
	indexKey     [32]byte
}

func (b *builder) buildDirectory(ctx context.Context, current directoryBuildContext, stats *buildStats) (cid.Cid, error) {
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	if current.root == nil {
		return cid.Undef, fmt.Errorf("encrypted UnixFS directory root is nil")
	}
	directory, err := current.root.Open(".")
	if err != nil {
		return cid.Undef, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return cid.Undef, err
	}
	if !info.IsDir() {
		return cid.Undef, fmt.Errorf("encrypted UnixFS directory is not a real directory: %s", current.relativePath)
	}
	writeSourceFingerprintNode(stats, current.relativePath, info)
	children, err := directory.ReadDir(-1)
	if err != nil {
		return cid.Undef, fmt.Errorf("read encrypted UnixFS directory %s: %w", current.relativePath, err)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	entries := make([]DirectoryEntry, 0, len(children))
	bindings := make(map[string]string, len(children)+1)
	for _, child := range children {
		if err := ctx.Err(); err != nil {
			return cid.Undef, err
		}
		name := child.Name()
		if err := validateEntryName(name); err != nil {
			return cid.Undef, fmt.Errorf("project encrypted UnixFS path %s: %w", path.Join(current.relativePath, name), err)
		}
		relative := name
		if current.relativePath != "" {
			relative = path.Join(current.relativePath, name)
		}
		childInfo, err := current.root.Lstat(name)
		if err != nil {
			return cid.Undef, fmt.Errorf("stat encrypted UnixFS path %s: %w", relative, err)
		}
		token := entryToken(current.indexKey, current.relativePath, name)
		entry := DirectoryEntry{Name: name, Token: token}
		var target cid.Cid
		switch {
		case childInfo.Mode()&os.ModeSymlink != 0:
			entry.Type = EntrySymlink
			target, err = b.buildSymlink(ctx, current, name, relative, childInfo, stats)
		case childInfo.IsDir():
			entry.Type = EntryDirectory
			childRoot, openErr := current.root.OpenRoot(name)
			if openErr != nil {
				return cid.Undef, fmt.Errorf("open encrypted UnixFS directory %s: %w", relative, openErr)
			}
			opened, openErr := childRoot.Open(".")
			if openErr != nil {
				_ = childRoot.Close()
				return cid.Undef, fmt.Errorf("pin encrypted UnixFS directory %s: %w", relative, openErr)
			}
			openedInfo, statErr := opened.Stat()
			closeErr := opened.Close()
			if statErr != nil || closeErr != nil || !openedInfo.IsDir() || !os.SameFile(childInfo, openedInfo) {
				_ = childRoot.Close()
				if statErr != nil || closeErr != nil {
					return cid.Undef, errors.Join(statErr, closeErr)
				}
				return cid.Undef, fmt.Errorf("encrypted UnixFS directory %s changed while it was opened", relative)
			}
			target, err = b.buildDirectory(ctx, directoryBuildContext{
				datasetID: current.datasetID, branch: current.branch, bindingID: current.bindingID,
				root: childRoot, relativePath: relative,
				epoch: current.epoch, key: current.key, indexKey: current.indexKey,
			}, stats)
			closeErr = childRoot.Close()
			if err == nil && closeErr != nil {
				err = closeErr
			}
		case childInfo.Mode().IsRegular():
			entry.Type = EntryFile
			target, err = b.buildFile(ctx, current, name, relative, childInfo, stats)
		default:
			err = fmt.Errorf("unsupported filesystem object in encrypted UnixFS binding: %s", relative)
		}
		if err != nil {
			return cid.Undef, err
		}
		if !target.Defined() {
			return cid.Undef, fmt.Errorf("encrypted UnixFS child target is undefined: %s", relative)
		}
		entries = append(entries, entry)
		bindings[token] = target.String()
	}
	manifest := DirectoryManifest{
		Profile: ProfileID, Version: ProfileVersion, Kind: EntryDirectory,
		Mode: uint32(info.Mode().Perm()), ModifiedUnixNano: info.ModTime().UnixNano(),
		Entries: canonicalDirectoryEntries(entries),
	}
	if err := validateDirectoryManifest(manifest); err != nil {
		return cid.Undef, err
	}
	plaintext, err := encodeCanonical(manifest)
	if err != nil {
		return cid.Undef, err
	}
	key := directoryManifestKey(current.key, current.relativePath)
	sealed, err := sealEnvelope(
		kindDirectoryManifest, current.epoch, key, plaintext,
		current.datasetID, current.branch, current.bindingID, current.relativePath,
	)
	if err != nil {
		return cid.Undef, err
	}
	manifestCID, err := b.putVerified(ctx, sealed)
	if err != nil {
		return cid.Undef, fmt.Errorf("store encrypted UnixFS directory manifest: %w", err)
	}
	bindings["@payload"] = manifestCID.String()
	root, err := b.graph.CreateStagedRoot(ctx, bindings)
	if err != nil {
		return cid.Undef, fmt.Errorf("materialize encrypted UnixFS directory root: %w", err)
	}
	if !root.Defined() {
		return cid.Undef, fmt.Errorf("encrypted UnixFS directory root is undefined")
	}
	stats.directories++
	stats.encryptedBytes += int64(len(sealed))
	stats.immutableBlocks++
	stats.maltObjects++
	return root, nil
}

func (b *builder) buildFile(ctx context.Context, parent directoryBuildContext, name, relative string, before fs.FileInfo, stats *buildStats) (cid.Cid, error) {
	file, err := parent.root.Open(name)
	if err != nil {
		return cid.Undef, fmt.Errorf("open encrypted UnixFS file %s: %w", relative, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return cid.Undef, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return cid.Undef, fmt.Errorf("encrypted UnixFS file %s changed while it was opened", relative)
	}
	writeSourceFingerprintNode(stats, relative, info)
	contentKey := fileContentKey(parent.key, relative)
	buffer := make([]byte, b.chunkSize)
	chunks := make([]cid.Cid, 0, max(1, int((info.Size()+int64(b.chunkSize)-1)/int64(b.chunkSize))))
	var totalCipher uint64
	index := uint64(0)
	for {
		n, readErr := io.ReadFull(file, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return cid.Undef, fmt.Errorf("read encrypted UnixFS file %s: %w", relative, readErr)
		}
		if n > 0 || (index == 0 && errors.Is(readErr, io.EOF)) {
			_, _ = stats.sourceHash.Write(buffer[:n])
			sealed, err := sealEnvelope(
				kindFileChunk, parent.epoch, contentKey, buffer[:n],
				chunkContext(parent.datasetID, parent.branch, parent.bindingID, relative, index)...,
			)
			if err != nil {
				return cid.Undef, err
			}
			chunkCID, err := b.putVerified(ctx, sealed)
			if err != nil {
				return cid.Undef, fmt.Errorf("store encrypted UnixFS file chunk: %w", err)
			}
			chunks = append(chunks, chunkCID)
			totalCipher += uint64(len(sealed))
			stats.encryptedBytes += int64(len(sealed))
			stats.immutableBlocks++
			index++
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if len(chunks) == 0 {
		return cid.Undef, fmt.Errorf("encrypted UnixFS file produced no chunks")
	}
	storage := StorageRaw
	contentRoot := chunks[0]
	cipherChunkSize := uint64(b.chunkSize + envelopeOverhead)
	if len(chunks) > 1 {
		storage = StorageList
		base, err := b.graph.CreateFixedListBaseRoot(ctx)
		if err != nil {
			return cid.Undef, fmt.Errorf("create encrypted UnixFS List base: %w", err)
		}
		mutation, err := unixfsmodel.FixedListPayloadMutation(base, chunks, totalCipher, cipherChunkSize)
		if err != nil {
			return cid.Undef, err
		}
		contentRoot, err = b.graph.ApplyFixedListPayloadMutation(ctx, mutation)
		if err != nil {
			return cid.Undef, fmt.Errorf("materialize encrypted UnixFS file List: %w", err)
		}
		if !contentRoot.Defined() {
			return cid.Undef, fmt.Errorf("encrypted UnixFS file List root is undefined")
		}
		stats.maltObjects++
	}
	manifest := FileManifest{
		Profile: ProfileID, Version: ProfileVersion, Kind: EntryFile,
		Mode: uint32(info.Mode().Perm()), ModifiedUnixNano: info.ModTime().UnixNano(), Size: uint64(info.Size()),
		Storage: storage, PlaintextChunkSize: uint64(b.chunkSize), CiphertextChunkSize: cipherChunkSize,
		CiphertextSize: totalCipher, ChunkCount: uint64(len(chunks)),
	}
	if err := validateFileManifest(manifest); err != nil {
		return cid.Undef, err
	}
	manifestCID, manifestBytes, err := b.storeFileManifest(ctx, parent, relative, manifest)
	if err != nil {
		return cid.Undef, err
	}
	root, err := b.graph.CreateStagedRoot(ctx, map[string]string{
		"@payload": manifestCID.String(), "@content": contentRoot.String(),
	})
	if err != nil {
		return cid.Undef, fmt.Errorf("materialize encrypted UnixFS file root: %w", err)
	}
	if !root.Defined() {
		return cid.Undef, fmt.Errorf("encrypted UnixFS file root is undefined")
	}
	stats.files++
	stats.encryptedBytes += int64(manifestBytes)
	stats.immutableBlocks++
	stats.maltObjects++
	return root, nil
}

func (b *builder) buildSymlink(ctx context.Context, parent directoryBuildContext, name, relative string, before fs.FileInfo, stats *buildStats) (cid.Cid, error) {
	target, err := parent.root.Readlink(name)
	if err != nil {
		return cid.Undef, fmt.Errorf("read encrypted UnixFS symlink %s: %w", relative, err)
	}
	after, err := parent.root.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink == 0 || !os.SameFile(before, after) {
		if err != nil {
			return cid.Undef, err
		}
		return cid.Undef, fmt.Errorf("encrypted UnixFS symlink %s changed while it was read", relative)
	}
	portableTarget, err := validateSymlinkTarget(relative, target)
	if err != nil {
		return cid.Undef, err
	}
	writeSourceFingerprintNode(stats, relative, after)
	writeSourceFingerprintField(stats.sourceHash, []byte(target))
	manifest := FileManifest{
		Profile: ProfileID, Version: ProfileVersion, Kind: EntrySymlink,
		Mode: uint32(after.Mode().Perm()), ModifiedUnixNano: after.ModTime().UnixNano(), LinkTarget: portableTarget,
	}
	if err := validateFileManifest(manifest); err != nil {
		return cid.Undef, err
	}
	manifestCID, manifestBytes, err := b.storeFileManifest(ctx, parent, relative, manifest)
	if err != nil {
		return cid.Undef, err
	}
	root, err := b.graph.CreateStagedRoot(ctx, map[string]string{"@payload": manifestCID.String()})
	if err != nil {
		return cid.Undef, fmt.Errorf("materialize encrypted UnixFS symlink root: %w", err)
	}
	if !root.Defined() {
		return cid.Undef, fmt.Errorf("encrypted UnixFS symlink root is undefined")
	}
	stats.files++
	stats.encryptedBytes += int64(manifestBytes)
	stats.immutableBlocks++
	stats.maltObjects++
	return root, nil
}

func writeSourceFingerprintNode(stats *buildStats, relative string, info fs.FileInfo) {
	displayPath := stats.sourceName
	if relative != "" {
		displayPath = filepath.Join(stats.sourceName, filepath.FromSlash(relative))
	}
	writeSourceFingerprintField(stats.sourceHash, []byte(filepath.ToSlash(displayPath)))
	var metadata [24]byte
	binary.BigEndian.PutUint64(metadata[0:8], uint64(info.Mode()))
	binary.BigEndian.PutUint64(metadata[8:16], uint64(info.Size()))
	binary.BigEndian.PutUint64(metadata[16:24], uint64(info.ModTime().UnixNano()))
	_, _ = stats.sourceHash.Write(metadata[:])
}

func writeSourceFingerprintField(target hash.Hash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = target.Write(size[:])
	_, _ = target.Write(value)
}

func (b *builder) storeFileManifest(ctx context.Context, parent directoryBuildContext, relative string, manifest FileManifest) (cid.Cid, int, error) {
	plaintext, err := encodeCanonical(manifest)
	if err != nil {
		return cid.Undef, 0, err
	}
	key := fileManifestKey(parent.key, relative)
	sealed, err := sealEnvelope(
		kindFileManifest, parent.epoch, key, plaintext,
		parent.datasetID, parent.branch, parent.bindingID, relative,
	)
	if err != nil {
		return cid.Undef, 0, err
	}
	manifestCID, err := b.putVerified(ctx, sealed)
	if err != nil {
		return cid.Undef, 0, fmt.Errorf("store encrypted UnixFS file manifest: %w", err)
	}
	return manifestCID, len(sealed), nil
}

func (b *builder) putVerified(ctx context.Context, data []byte) (cid.Cid, error) {
	key, err := b.blocks.Put(ctx, data)
	if err != nil {
		return cid.Undef, err
	}
	if !key.Defined() {
		return cid.Undef, fmt.Errorf("block writer returned an undefined CID")
	}
	computed, err := key.Prefix().Sum(data)
	if err != nil {
		return cid.Undef, err
	}
	if !computed.Equals(key) {
		return cid.Undef, fmt.Errorf("block writer returned CID %s for different bytes", key)
	}
	return key, nil
}

func validateSymlinkTarget(relativePath, target string) (string, error) {
	if target == "" || filepath.IsAbs(target) || filepath.VolumeName(target) != "" || strings.Contains(target, "\\") {
		return "", fmt.Errorf("encrypted UnixFS symlink target must be a portable relative path: %s", relativePath)
	}
	joined := path.Clean(path.Join(path.Dir(relativePath), target))
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return "", fmt.Errorf("encrypted UnixFS symlink escapes its binding: %s", relativePath)
	}
	portable := path.Clean(filepath.ToSlash(target))
	if portable == "." || strings.HasPrefix(portable, "/") {
		return "", fmt.Errorf("encrypted UnixFS symlink target is invalid: %s", relativePath)
	}
	return portable, nil
}

func chunkPlaintextLength(manifest FileManifest, index uint64) (uint64, error) {
	if err := validateFileManifest(manifest); err != nil {
		return 0, err
	}
	if index >= manifest.ChunkCount {
		return 0, fmt.Errorf("encrypted UnixFS chunk index %d is out of range", index)
	}
	if index+1 < manifest.ChunkCount {
		return manifest.PlaintextChunkSize, nil
	}
	if manifest.Size == 0 {
		return 0, nil
	}
	last := manifest.Size % manifest.PlaintextChunkSize
	if last == 0 {
		last = manifest.PlaintextChunkSize
	}
	return last, nil
}

func chunkCiphertextLength(manifest FileManifest, index uint64) (uint64, error) {
	plain, err := chunkPlaintextLength(manifest, index)
	if err != nil {
		return 0, err
	}
	return plain + envelopeOverhead, nil
}

func chunkOffset(manifest FileManifest, index uint64) (uint64, error) {
	if index >= manifest.ChunkCount {
		return 0, fmt.Errorf("encrypted UnixFS chunk index %d is out of range", index)
	}
	return index * manifest.CiphertextChunkSize, nil
}

func stableIndex(index uint64) string { return strconv.FormatUint(index, 10) }
