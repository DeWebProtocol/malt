package encrypted

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	malt "github.com/dewebprotocol/malt-core"
	"github.com/dewebprotocol/malt-core/protocol"
	clientverifier "github.com/dewebprotocol/malt-core/sdk/verifier"
	cid "github.com/ipfs/go-cid"
)

var (
	ErrNotFound     = errors.New("encrypted UnixFS path not found")
	ErrWrongProfile = errors.New("root is not the encrypted UnixFS profile")
)

type KeyResolver func(epoch uint32) ([32]byte, error)

type ReaderOptions struct {
	Remote   unixfs.Remote
	Blocks   unixfs.BlockGetter
	Verifier unixfs.LocalVerifier
}

type Reader struct {
	remote   unixfs.Remote
	blocks   unixfs.BlockGetter
	verifier unixfs.LocalVerifier
	lists    unixfs.Reader
}

type DatasetView struct {
	Root        cid.Cid
	ManifestCID cid.Cid
	Epoch       uint32
	Manifest    DatasetManifest
	Bindings    []BindingView
	verified    *verifiedDataset
}

// verifiedDataset is the immutable authority behind a DatasetView. The
// exported fields remain as read-oriented compatibility snapshots, but no
// verifier or manifest-reuse path trusts those caller-mutable copies.
type verifiedDataset struct {
	root        cid.Cid
	manifestCID cid.Cid
	epoch       uint32
	manifest    DatasetManifest
	bindings    []BindingView
}

type BindingView struct {
	Manifest BindingManifest
	Root     cid.Cid
}

type DirectoryView struct {
	Root         cid.Cid
	ManifestCID  cid.Cid
	Epoch        uint32
	Manifest     DirectoryManifest
	DatasetID    string
	Branch       string
	BindingID    string
	RelativePath string
}

type FileView struct {
	Root         cid.Cid
	ManifestCID  cid.Cid
	Content      cid.Cid
	Epoch        uint32
	Manifest     FileManifest
	DatasetID    string
	Branch       string
	BindingID    string
	RelativePath string
}

func NewReader(opts ReaderOptions) (*Reader, error) {
	if opts.Remote == nil || opts.Blocks == nil {
		return nil, fmt.Errorf("encrypted UnixFS remote and block reader are required")
	}
	verifier := opts.Verifier
	if verifier == nil {
		var err error
		verifier, err = clientverifier.NewDefault()
		if err != nil {
			return nil, fmt.Errorf("initialize encrypted UnixFS verifier: %w", err)
		}
	}
	lists, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: opts.Remote, Blocks: opts.Blocks, Verifier: verifier})
	if err != nil {
		return nil, err
	}
	return &Reader{remote: opts.Remote, blocks: opts.Blocks, verifier: verifier, lists: lists}, nil
}

// LoadDataset verifies and decrypts the root manifest, then verifies every
// opaque binding token against the caller-selected root. No remote head is
// accepted by this operation.
func (r *Reader) LoadDataset(ctx context.Context, root cid.Cid, datasetID, branch string, keys KeyResolver) (*DatasetView, error) {
	if r == nil || !root.Defined() || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(branch) == "" || keys == nil {
		return nil, fmt.Errorf("encrypted UnixFS dataset read request is incomplete")
	}
	manifestCID, err := r.resolve(ctx, root, []string{"@payload"})
	if err != nil {
		return nil, fmt.Errorf("resolve encrypted UnixFS dataset manifest: %w", err)
	}
	sealed, err := r.getBoundBlock(ctx, manifestCID)
	if err != nil {
		return nil, fmt.Errorf("fetch encrypted UnixFS dataset manifest: %w", err)
	}
	parsed, err := parseEnvelope(sealed)
	if err != nil || parsed.Kind != kindDatasetManifest {
		return nil, fmt.Errorf("%w: dataset manifest envelope is invalid", ErrWrongProfile)
	}
	bucketKey, err := keys(parsed.Epoch)
	if err != nil {
		return nil, err
	}
	indexKey, err := keys(NamespaceKeyEpoch)
	if err != nil {
		return nil, fmt.Errorf("load encrypted UnixFS namespace key: %w", err)
	}
	key := datasetManifestKey(bucketKey, datasetID, branch)
	plaintext, manifestEpoch, err := openEnvelope(sealed, kindDatasetManifest, key, datasetID, branch)
	if err != nil {
		return nil, err
	}
	var manifest DatasetManifest
	if err := decodeStrict(plaintext, &manifest); err != nil {
		return nil, fmt.Errorf("decode encrypted UnixFS dataset manifest: %w", err)
	}
	if err := validateDatasetManifest(manifest); err != nil {
		return nil, err
	}
	if manifest.DatasetID != datasetID || manifest.Branch != branch {
		return nil, fmt.Errorf("encrypted UnixFS dataset manifest identity mismatch")
	}
	bindings := make([]BindingView, len(manifest.Bindings))
	for index, binding := range manifest.Bindings {
		if binding.Token != bindingToken(indexKey, datasetID, branch, binding.ID) {
			return nil, fmt.Errorf("encrypted UnixFS binding %q token is not derived from this dataset", binding.ID)
		}
		target, err := r.resolve(ctx, root, []string{binding.Token})
		if err != nil {
			return nil, fmt.Errorf("verify encrypted UnixFS binding %q: %w", binding.ID, err)
		}
		bindings[index] = BindingView{Manifest: binding, Root: target}
	}
	return newVerifiedDatasetView(root, manifestCID, manifestEpoch, manifest, bindings), nil
}

func (v *DatasetView) Binding(id string) (BindingView, bool) {
	verified, err := v.verifiedSnapshot()
	if err != nil {
		return BindingView{}, false
	}
	index, found := slices.BinarySearchFunc(verified.bindings, id, func(binding BindingView, target string) int {
		return strings.Compare(binding.Manifest.ID, target)
	})
	if !found {
		return BindingView{}, false
	}
	return verified.bindings[index], true
}

// VerifiedRoot returns the locally verified dataset Root. It never consults
// the caller-mutable compatibility fields on DatasetView.
func (v *DatasetView) VerifiedRoot() (cid.Cid, error) {
	verified, err := v.verifiedSnapshot()
	if err != nil {
		return cid.Undef, err
	}
	return verified.root, nil
}

// VerifiedManifestCID returns the CID bound to @payload by the verified Root.
func (v *DatasetView) VerifiedManifestCID() (cid.Cid, error) {
	verified, err := v.verifiedSnapshot()
	if err != nil {
		return cid.Undef, err
	}
	return verified.manifestCID, nil
}

// VerifiedManifest returns an independent copy of the decrypted manifest.
func (v *DatasetView) VerifiedManifest() (DatasetManifest, error) {
	verified, err := v.verifiedSnapshot()
	if err != nil {
		return DatasetManifest{}, err
	}
	return cloneDatasetManifest(verified.manifest), nil
}

func newVerifiedDatasetView(root, manifestCID cid.Cid, epoch uint32, manifest DatasetManifest, bindings []BindingView) *DatasetView {
	sealed := &verifiedDataset{
		root: root, manifestCID: manifestCID, epoch: epoch,
		manifest: cloneDatasetManifest(manifest), bindings: append([]BindingView(nil), bindings...),
	}
	return &DatasetView{
		Root: root, ManifestCID: manifestCID, Epoch: epoch,
		Manifest: cloneDatasetManifest(manifest), Bindings: append([]BindingView(nil), bindings...), verified: sealed,
	}
}

func (v *DatasetView) verifiedSnapshot() (*verifiedDataset, error) {
	if v == nil || v.verified == nil || !v.verified.root.Defined() || !v.verified.manifestCID.Defined() {
		return nil, fmt.Errorf("encrypted UnixFS dataset view is not locally verified")
	}
	return v.verified, nil
}

func cloneDatasetManifest(value DatasetManifest) DatasetManifest {
	value.Bindings = append([]BindingManifest(nil), value.Bindings...)
	return value
}

// OpenDirectory returns a locally verified and decrypted readdir view. The
// remote executor never enumerates or interprets opaque child tokens.
func (r *Reader) OpenDirectory(ctx context.Context, dataset *DatasetView, bindingID, relativePath string, keys KeyResolver) (*DirectoryView, error) {
	if dataset == nil || keys == nil {
		return nil, fmt.Errorf("encrypted UnixFS directory request is incomplete")
	}
	verified, err := dataset.verifiedSnapshot()
	if err != nil {
		return nil, err
	}
	binding, ok := dataset.Binding(bindingID)
	if !ok {
		return nil, fmt.Errorf("%w: binding %s", ErrNotFound, bindingID)
	}
	segments, err := parseRelativePath(relativePath)
	if err != nil {
		return nil, err
	}
	currentRoot := binding.Root
	currentPath := ""
	for _, segment := range segments {
		directory, err := r.openDirectoryAt(ctx, verified.manifest.DatasetID, verified.manifest.Branch, bindingID, currentPath, currentRoot, keys)
		if err != nil {
			return nil, err
		}
		entry, ok := findDirectoryEntry(directory.Manifest.Entries, segment)
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path.Join(currentPath, segment))
		}
		if entry.Type != EntryDirectory {
			return nil, fmt.Errorf("encrypted UnixFS path is not a directory: %s", path.Join(currentPath, segment))
		}
		currentRoot, err = r.resolve(ctx, currentRoot, []string{entry.Token})
		if err != nil {
			return nil, err
		}
		currentPath = path.Join(currentPath, segment)
	}
	return r.openDirectoryAt(ctx, verified.manifest.DatasetID, verified.manifest.Branch, bindingID, currentPath, currentRoot, keys)
}

// OpenFile resolves a plaintext path only through decrypted parent manifests,
// then verifies the manifest and content bindings at the selected file root.
func (r *Reader) OpenFile(ctx context.Context, dataset *DatasetView, bindingID, relativePath string, keys KeyResolver) (*FileView, error) {
	verified, err := dataset.verifiedSnapshot()
	if err != nil {
		return nil, err
	}
	segments, err := parseRelativePath(relativePath)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		return nil, fmt.Errorf("encrypted UnixFS file path is empty")
	}
	parentPath := strings.Join(segments[:len(segments)-1], "/")
	parent, err := r.OpenDirectory(ctx, dataset, bindingID, parentPath, keys)
	if err != nil {
		return nil, err
	}
	entry, ok := findDirectoryEntry(parent.Manifest.Entries, segments[len(segments)-1])
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, relativePath)
	}
	if entry.Type != EntryFile && entry.Type != EntrySymlink {
		return nil, fmt.Errorf("encrypted UnixFS path is not a file: %s", relativePath)
	}
	fileRoot, err := r.resolve(ctx, parent.Root, []string{entry.Token})
	if err != nil {
		return nil, err
	}
	return r.openFileAt(ctx, verified.manifest.DatasetID, verified.manifest.Branch, bindingID, strings.Join(segments, "/"), entry.Type, fileRoot, keys)
}

func (r *Reader) ReadFile(ctx context.Context, dataset *DatasetView, bindingID, relativePath string, keys KeyResolver) ([]byte, error) {
	file, err := r.OpenFile(ctx, dataset, bindingID, relativePath, keys)
	if err != nil {
		return nil, err
	}
	if file.Manifest.Kind != EntryFile {
		return nil, fmt.Errorf("encrypted UnixFS path is not a regular file: %s", relativePath)
	}
	if file.Manifest.Size > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("encrypted UnixFS file is too large to materialize in memory")
	}
	result := make([]byte, 0, int(file.Manifest.Size))
	for index := uint64(0); index < file.Manifest.ChunkCount; index++ {
		chunk, err := r.readChunk(ctx, *file, index, keys)
		if err != nil {
			return nil, err
		}
		result = append(result, chunk...)
	}
	if uint64(len(result)) != file.Manifest.Size {
		return nil, fmt.Errorf("encrypted UnixFS file plaintext size mismatch")
	}
	return result, nil
}

func (r *Reader) ReadFileRange(ctx context.Context, dataset *DatasetView, bindingID, relativePath string, offset, length uint64, keys KeyResolver) ([]byte, error) {
	file, err := r.OpenFile(ctx, dataset, bindingID, relativePath, keys)
	if err != nil {
		return nil, err
	}
	if file.Manifest.Kind != EntryFile || offset > file.Manifest.Size || length > file.Manifest.Size-offset {
		return nil, fmt.Errorf("invalid encrypted UnixFS file range")
	}
	if length == 0 {
		return []byte{}, nil
	}
	if length > uint64(int(^uint(0)>>1)) {
		return nil, fmt.Errorf("encrypted UnixFS range is too large to materialize in memory")
	}
	first := offset / file.Manifest.PlaintextChunkSize
	last := (offset + length - 1) / file.Manifest.PlaintextChunkSize
	result := make([]byte, 0, int(length))
	for index := first; index <= last; index++ {
		chunk, err := r.readChunk(ctx, *file, index, keys)
		if err != nil {
			return nil, err
		}
		chunkStart := index * file.Manifest.PlaintextChunkSize
		start := uint64(0)
		if offset > chunkStart {
			start = offset - chunkStart
		}
		end := uint64(len(chunk))
		if requestedEnd := offset + length; requestedEnd < chunkStart+end {
			end = requestedEnd - chunkStart
		}
		result = append(result, chunk[start:end]...)
	}
	return result, nil
}

// RestoreBinding materializes one verified binding into an absent destination
// tree. It verifies every manifest proof and ciphertext CID before decryption.
func (r *Reader) RestoreBinding(ctx context.Context, dataset *DatasetView, bindingID, destination string, keys KeyResolver) error {
	if dataset == nil || keys == nil || destination == "" {
		return fmt.Errorf("encrypted UnixFS restore request is incomplete")
	}
	if _, err := dataset.verifiedSnapshot(); err != nil {
		return err
	}
	if _, ok := dataset.Binding(bindingID); !ok {
		return fmt.Errorf("%w: binding %s", ErrNotFound, bindingID)
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("encrypted UnixFS restore destination is not a safe directory")
		}
		entries, err := os.ReadDir(destination)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return fmt.Errorf("encrypted UnixFS restore destination is not empty")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
	} else {
		return err
	}
	rootInfo, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	restoreRoot, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open encrypted UnixFS restore root: %w", err)
	}
	defer restoreRoot.Close()
	opened, err := restoreRoot.Open(".")
	if err != nil {
		return err
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil {
		return errors.Join(statErr, closeErr)
	}
	if !openedInfo.IsDir() || !os.SameFile(rootInfo, openedInfo) {
		return fmt.Errorf("encrypted UnixFS restore root changed while it was opened")
	}
	return r.RestoreBindingRoot(ctx, dataset, bindingID, restoreRoot, keys)
}

// RestoreBindingRoot materializes into an already pinned, empty destination.
// The caller retains ownership of restoreRoot.
func (r *Reader) RestoreBindingRoot(ctx context.Context, dataset *DatasetView, bindingID string, restoreRoot *os.Root, keys KeyResolver) error {
	if dataset == nil || keys == nil || restoreRoot == nil {
		return fmt.Errorf("encrypted UnixFS rooted restore request is incomplete")
	}
	if _, err := dataset.verifiedSnapshot(); err != nil {
		return err
	}
	if _, ok := dataset.Binding(bindingID); !ok {
		return fmt.Errorf("%w: binding %s", ErrNotFound, bindingID)
	}
	opened, err := restoreRoot.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := opened.ReadDir(-1)
	closeErr := opened.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if len(entries) != 0 {
		return fmt.Errorf("encrypted UnixFS rooted restore destination is not empty")
	}
	directory, err := r.OpenDirectory(ctx, dataset, bindingID, "", keys)
	if err != nil {
		return err
	}
	return r.restoreDirectory(ctx, dataset, directory, restoreRoot, keys)
}

func (r *Reader) restoreDirectory(ctx context.Context, dataset *DatasetView, directory *DirectoryView, restoreRoot *os.Root, keys KeyResolver) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, entry := range directory.Manifest.Entries {
		relative := path.Join(directory.RelativePath, entry.Name)
		target := filepath.FromSlash(relative)
		childRoot, err := r.resolve(ctx, directory.Root, []string{entry.Token})
		if err != nil {
			return err
		}
		switch entry.Type {
		case EntryDirectory:
			if err := restoreRoot.Mkdir(target, 0o700); err != nil {
				return err
			}
			child, err := r.openDirectoryAt(ctx, directory.DatasetID, directory.Branch, directory.BindingID, relative, childRoot, keys)
			if err != nil {
				return err
			}
			if err := r.restoreDirectory(ctx, dataset, child, restoreRoot, keys); err != nil {
				return err
			}
		case EntryFile, EntrySymlink:
			file, err := r.openFileAt(ctx, directory.DatasetID, directory.Branch, directory.BindingID, relative, entry.Type, childRoot, keys)
			if err != nil {
				return err
			}
			if entry.Type == EntrySymlink {
				if _, err := validateSymlinkTarget(relative, file.Manifest.LinkTarget); err != nil {
					return err
				}
				if err := restoreRoot.Symlink(filepath.FromSlash(file.Manifest.LinkTarget), target); err != nil {
					return err
				}
				continue
			}
			if err := r.restoreRegularFile(ctx, *file, restoreRoot, target, keys); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported encrypted UnixFS restore entry type %q", entry.Type)
		}
	}
	directoryPath := filepath.FromSlash(directory.RelativePath)
	if directoryPath == "" {
		directoryPath = "."
	}
	directoryHandle, err := restoreRoot.Open(directoryPath)
	if err != nil {
		return err
	}
	directoryInfo, err := directoryHandle.Stat()
	if err != nil || !directoryInfo.IsDir() {
		_ = directoryHandle.Close()
		if err == nil {
			err = fmt.Errorf("restored directory changed before it was persisted: %s", directoryPath)
		}
		return err
	}
	if err := restoreRoot.Chmod(directoryPath, os.FileMode(directory.Manifest.Mode)&0o777); err != nil {
		_ = directoryHandle.Close()
		return err
	}
	modified := time.Unix(0, directory.Manifest.ModifiedUnixNano)
	if err := restoreRoot.Chtimes(directoryPath, modified, modified); err != nil {
		_ = directoryHandle.Close()
		return err
	}
	currentInfo, statErr := restoreRoot.Lstat(directoryPath)
	if statErr == nil && (currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.IsDir() || !os.SameFile(directoryInfo, currentInfo)) {
		statErr = fmt.Errorf("restored directory changed before it was persisted: %s", directoryPath)
	}
	var syncErr error
	if statErr == nil {
		syncErr = directoryHandle.Sync()
	}
	closeErr := directoryHandle.Close()
	return errors.Join(statErr, syncErr, closeErr)
}

func (r *Reader) restoreRegularFile(ctx context.Context, file FileView, restoreRoot *os.Root, target string, keys KeyResolver) error {
	out, err := restoreRoot.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = restoreRoot.Remove(target)
		}
	}()
	for index := uint64(0); index < file.Manifest.ChunkCount; index++ {
		chunk, err := r.readChunk(ctx, file, index, keys)
		if err != nil {
			return err
		}
		if _, err := out.Write(chunk); err != nil {
			return err
		}
	}
	if err := out.Chmod(os.FileMode(file.Manifest.Mode) & 0o777); err != nil {
		return err
	}
	modified := time.Unix(0, file.Manifest.ModifiedUnixNano)
	if err := restoreRoot.Chtimes(target, modified, modified); err != nil {
		return err
	}
	currentInfo, statErr := restoreRoot.Lstat(target)
	openedInfo, openedStatErr := out.Stat()
	if err := errors.Join(statErr, openedStatErr); err != nil {
		return err
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() || !openedInfo.Mode().IsRegular() || !os.SameFile(currentInfo, openedInfo) {
		return fmt.Errorf("restored file changed before it was persisted: %s", target)
	}
	// Persist content, mode, and timestamps through the still-pinned handle.
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func (r *Reader) openDirectoryAt(ctx context.Context, datasetID, branch, bindingID, relativePath string, root cid.Cid, keys KeyResolver) (*DirectoryView, error) {
	manifestCID, err := r.resolve(ctx, root, []string{"@payload"})
	if err != nil {
		return nil, fmt.Errorf("resolve encrypted UnixFS directory manifest: %w", err)
	}
	sealed, err := r.getBoundBlock(ctx, manifestCID)
	if err != nil {
		return nil, err
	}
	parsed, err := parseEnvelope(sealed)
	if err != nil || parsed.Kind != kindDirectoryManifest {
		return nil, fmt.Errorf("encrypted UnixFS directory manifest envelope is invalid")
	}
	bucketKey, err := keys(parsed.Epoch)
	if err != nil {
		return nil, err
	}
	indexKey, err := keys(NamespaceKeyEpoch)
	if err != nil {
		return nil, fmt.Errorf("load encrypted UnixFS namespace key: %w", err)
	}
	key := directoryManifestKey(bindingKey(bucketKey, datasetID, branch, bindingID), relativePath)
	plaintext, manifestEpoch, err := openEnvelope(sealed, kindDirectoryManifest, key, datasetID, branch, bindingID, relativePath)
	if err != nil {
		return nil, err
	}
	var manifest DirectoryManifest
	if err := decodeStrict(plaintext, &manifest); err != nil {
		return nil, fmt.Errorf("decode encrypted UnixFS directory manifest: %w", err)
	}
	if err := validateDirectoryManifest(manifest); err != nil {
		return nil, err
	}
	bindingSecret := bindingKey(indexKey, datasetID, branch, bindingID)
	for _, entry := range manifest.Entries {
		if entry.Token != entryToken(bindingSecret, relativePath, entry.Name) {
			return nil, fmt.Errorf("encrypted UnixFS entry %q token is not derived from its parent", entry.Name)
		}
	}
	return &DirectoryView{
		Root: root, ManifestCID: manifestCID, Epoch: manifestEpoch, Manifest: manifest,
		DatasetID: datasetID, Branch: branch, BindingID: bindingID, RelativePath: relativePath,
	}, nil
}

func (r *Reader) openFileAt(ctx context.Context, datasetID, branch, bindingID, relativePath, entryType string, root cid.Cid, keys KeyResolver) (*FileView, error) {
	manifestCID, err := r.resolve(ctx, root, []string{"@payload"})
	if err != nil {
		return nil, fmt.Errorf("resolve encrypted UnixFS file manifest: %w", err)
	}
	sealed, err := r.getBoundBlock(ctx, manifestCID)
	if err != nil {
		return nil, err
	}
	parsed, err := parseEnvelope(sealed)
	if err != nil || parsed.Kind != kindFileManifest {
		return nil, fmt.Errorf("encrypted UnixFS file manifest envelope is invalid")
	}
	bucketKey, err := keys(parsed.Epoch)
	if err != nil {
		return nil, err
	}
	key := fileManifestKey(bindingKey(bucketKey, datasetID, branch, bindingID), relativePath)
	plaintext, manifestEpoch, err := openEnvelope(sealed, kindFileManifest, key, datasetID, branch, bindingID, relativePath)
	if err != nil {
		return nil, err
	}
	var manifest FileManifest
	if err := decodeStrict(plaintext, &manifest); err != nil {
		return nil, fmt.Errorf("decode encrypted UnixFS file manifest: %w", err)
	}
	if err := validateFileManifest(manifest); err != nil {
		return nil, err
	}
	if manifest.Kind != entryType {
		return nil, fmt.Errorf("encrypted UnixFS parent/file type mismatch")
	}
	view := &FileView{
		Root: root, ManifestCID: manifestCID, Epoch: manifestEpoch, Manifest: manifest,
		DatasetID: datasetID, Branch: branch, BindingID: bindingID, RelativePath: relativePath,
	}
	if manifest.Kind == EntryFile {
		content, err := r.resolve(ctx, root, []string{"@content"})
		if err != nil {
			return nil, fmt.Errorf("resolve encrypted UnixFS file content: %w", err)
		}
		if unixfsmodel.StorageKindFromCID(content) != manifest.Storage {
			return nil, fmt.Errorf("encrypted UnixFS file storage kind does not match its authenticated content")
		}
		view.Content = content
	}
	return view, nil
}

func (r *Reader) readChunk(ctx context.Context, file FileView, index uint64, keys KeyResolver) ([]byte, error) {
	length, err := chunkCiphertextLength(file.Manifest, index)
	if err != nil {
		return nil, err
	}
	var sealed []byte
	switch file.Manifest.Storage {
	case StorageRaw:
		if index != 0 {
			return nil, fmt.Errorf("raw encrypted UnixFS file has more than one chunk")
		}
		sealed, err = r.getBoundBlock(ctx, file.Content)
	case StorageList:
		offset, offsetErr := chunkOffset(file.Manifest, index)
		if offsetErr != nil {
			return nil, offsetErr
		}
		part, readErr := r.lists.ReadListPayloadRange(ctx, file.Content, offset, length)
		if readErr != nil {
			return nil, readErr
		}
		if part.Offset != offset || uint64(len(part.Body)) != length || part.TotalSize != file.Manifest.CiphertextSize {
			return nil, fmt.Errorf("verified encrypted UnixFS List range has inconsistent geometry")
		}
		sealed = part.Body
	default:
		return nil, fmt.Errorf("unsupported encrypted UnixFS storage kind %q", file.Manifest.Storage)
	}
	if err != nil {
		return nil, err
	}
	if uint64(len(sealed)) != length {
		return nil, fmt.Errorf("encrypted UnixFS ciphertext chunk length mismatch")
	}
	parsed, err := parseEnvelope(sealed)
	if err != nil || parsed.Kind != kindFileChunk {
		return nil, fmt.Errorf("encrypted UnixFS file chunk envelope is invalid")
	}
	if parsed.Epoch != file.Epoch {
		return nil, fmt.Errorf("encrypted UnixFS file manifest and chunk epochs do not match")
	}
	bucketKey, err := keys(parsed.Epoch)
	if err != nil {
		return nil, err
	}
	key := fileContentKey(bindingKey(bucketKey, file.DatasetID, file.Branch, file.BindingID), file.RelativePath)
	plaintext, _, err := openEnvelope(
		sealed, kindFileChunk, key,
		chunkContext(file.DatasetID, file.Branch, file.BindingID, file.RelativePath, index)...,
	)
	if err != nil {
		return nil, err
	}
	want, err := chunkPlaintextLength(file.Manifest, index)
	if err != nil {
		return nil, err
	}
	if uint64(len(plaintext)) != want {
		return nil, fmt.Errorf("encrypted UnixFS plaintext chunk length mismatch")
	}
	return plaintext, nil
}

func (r *Reader) resolve(ctx context.Context, root cid.Cid, segments []string) (cid.Cid, error) {
	request, err := protocol.NewResolveRequest(malt.ResolveRequest{Root: root, Segments: append([]string(nil), segments...)})
	if err != nil {
		return cid.Undef, err
	}
	result, err := r.remote.Resolve(ctx, request)
	if err != nil {
		return cid.Undef, err
	}
	if result == nil {
		return cid.Undef, fmt.Errorf("untrusted executor returned a nil resolve result")
	}
	if err := r.verifier.VerifyResolve(ctx, protocol.ResolveVerification{Request: request, Result: *result}); err != nil {
		return cid.Undef, fmt.Errorf("verify encrypted UnixFS resolve locally: %w", err)
	}
	target, err := cid.Parse(result.Target)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode encrypted UnixFS resolve target: %w", err)
	}
	return target, nil
}

func (r *Reader) getBoundBlock(ctx context.Context, key cid.Cid) ([]byte, error) {
	if !key.Defined() {
		return nil, fmt.Errorf("encrypted UnixFS payload CID is undefined")
	}
	body, err := r.blocks.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	computed, err := key.Prefix().Sum(body)
	if err != nil {
		return nil, err
	}
	if !computed.Equals(key) {
		return nil, fmt.Errorf("encrypted UnixFS payload bytes do not match authenticated CID %s", key)
	}
	return body, nil
}

func findDirectoryEntry(entries []DirectoryEntry, name string) (DirectoryEntry, bool) {
	index, found := slices.BinarySearchFunc(entries, name, func(entry DirectoryEntry, target string) int {
		return strings.Compare(entry.Name, target)
	})
	if !found {
		return DirectoryEntry{}, false
	}
	return entries[index], true
}

func parseRelativePath(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	segments, err := unixfsmodel.ParsePath(raw)
	if err != nil {
		return nil, err
	}
	if strings.Join(segments, "/") != raw {
		return nil, fmt.Errorf("encrypted UnixFS path is not canonical: %q", raw)
	}
	return segments, nil
}
