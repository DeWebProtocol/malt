package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

const addFixedChunkSize = 262144

var (
	addBucketIDFlag     string
	addCreateBucketFlag bool
	addPrefixFlag       string
	addWrapFlag         bool
	addWrapNameFlag     string
)

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().StringVarP(&addBucketIDFlag, "bucket", "b", "", "Target bucket ID (defaults to client.default_bucket_id)")
	addCmd.Flags().BoolVar(&addCreateBucketFlag, "create-bucket", false, "Auto-create the bucket if it does not exist")
	addCmd.Flags().StringVarP(&addPrefixFlag, "prefix", "p", "", "Prefix inside the bucket")
	addCmd.Flags().BoolVarP(&addWrapFlag, "wrap", "w", false, "Wrap all inputs under one directory")
	addCmd.Flags().StringVar(&addWrapNameFlag, "wrap-name", "", "Wrapper directory name (required for multi-input --wrap)")
}

var addCmd = &cobra.Command{
	Use:   "add <local-path> [<local-path>...]",
	Short: "Upload local files/directories and merge into a bucket tree",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAdd,
}

type addSummary struct {
	Bucket      string `json:"bucket"`
	OldRoot     string `json:"old_root,omitempty"`
	NewRoot     string `json:"new_root"`
	Files       int    `json:"files_imported"`
	Bytes       int64  `json:"bytes_uploaded"`
	AutoCreated bool   `json:"bucket_auto_created"`
}

type addNode struct {
	Kind        string
	StorageKind string
	Key         cid.Cid
	Chunks      []cid.Cid
	Children    map[string]*addNode
	Changed     bool
}

func newDirNode() *addNode {
	return &addNode{
		Kind:     "dir",
		Children: make(map[string]*addNode),
	}
}

type addInput struct {
	Original string
	AbsPath  string
	BaseName string
	Info     fs.FileInfo
}

type addMountedInput struct {
	Input     addInput
	MountBase string
}

type addBuildResult struct {
	Root  *addNode
	Files int
	Bytes int64
}

type addMaterializeResult struct {
	Key         cid.Cid
	ArcCount    int
	Descendants map[string]cid.Cid
}

type addUnixFSResult struct {
	Files   int
	Bytes   int64
	NewRoot string
}

type addCASClient interface {
	Put(ctx context.Context, data []byte) (cid.Cid, error)
	PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error)
	Get(ctx context.Context, c cid.Cid) ([]byte, error)
}

func runAdd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	bucketID, err := resolveAddBucketID(cfg.Client.DefaultBucketID, addBucketIDFlag)
	if err != nil {
		return err
	}

	daemon := mustDaemonClient()
	meta, autoCreated, err := ensureAddBucket(ctx, daemon, bucketID, addCreateBucketFlag)
	if err != nil {
		return daemonCommandError(err)
	}

	oldRoot := strings.TrimSpace(meta.Root)
	casClient, err := makeCASClient()
	if err != nil {
		return err
	}
	result, err := addInputsWithUnixFS(ctx, daemon, casClient, bucketID, args, addBuildOptions{
		Prefix:   addPrefixFlag,
		Wrap:     addWrapFlag,
		WrapName: addWrapNameFlag,
	})
	if err != nil {
		var apiErr *daemonclient.Error
		if errors.As(err, &apiErr) {
			return daemonCommandError(err)
		}
		return err
	}
	if result.NewRoot == "" {
		return fmt.Errorf("failed to materialize a new bucket root")
	}

	printJSON(&addSummary{
		Bucket:      bucketID,
		OldRoot:     oldRoot,
		NewRoot:     result.NewRoot,
		Files:       result.Files,
		Bytes:       result.Bytes,
		AutoCreated: autoCreated,
	})
	return nil
}

type addBuildOptions struct {
	Prefix   string
	Wrap     bool
	WrapName string
}

func addInputsWithUnixFS(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, bucketID string, rawInputs []string, opts addBuildOptions) (*addUnixFSResult, error) {
	inputs, err := collectAddInputs(rawInputs)
	if err != nil {
		return nil, err
	}
	mounted, err := mountAddInputs(inputs, opts)
	if err != nil {
		return nil, err
	}

	root := newDirNode()
	result := &addUnixFSResult{}
	for _, item := range mounted {
		if item.Input.Info.IsDir() {
			files, bytesUploaded, err := stageFlatUnixFSDirectory(ctx, root, casClient, item)
			if err != nil {
				return nil, err
			}
			result.Files += files
			result.Bytes += bytesUploaded
			continue
		}
		bytesUploaded, err := stageFlatUnixFSFile(ctx, root, casClient, item.Input.AbsPath, item.MountBase)
		if err != nil {
			return nil, err
		}
		result.Files++
		result.Bytes += bytesUploaded
	}

	entries, err := flatUnixFSBatchEntries(ctx, casClient, root)
	if err != nil {
		return nil, err
	}
	meta, err := daemon.GetBucket(ctx, bucketID)
	if err != nil {
		return nil, err
	}
	resp, err := daemon.ApplyBucketUnixFSBatch(ctx, bucketID, &httpapi.BucketUnixFSBatchRequest{
		BaseRoot: meta.Root,
		Entries:  entries,
	})
	if err != nil {
		return nil, err
	}
	result.NewRoot = resp.NewRoot
	return result, nil
}

func stageFlatUnixFSDirectory(ctx context.Context, root *addNode, casClient addCASClient, item addMountedInput) (int, int64, error) {
	mountBase := canonicalAddPath(item.MountBase)
	if mountBase == "" {
		return 0, 0, fmt.Errorf("directory mount path must not be empty")
	}
	ensureDirNode(root, mountBase)

	var files int
	var bytesUploaded int64
	err := filepath.WalkDir(item.Input.AbsPath, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current != item.Input.AbsPath && d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not supported: %s", current)
		}
		rel, err := filepath.Rel(item.Input.AbsPath, current)
		if err != nil {
			return fmt.Errorf("compute relative path %q: %w", current, err)
		}
		if rel == "." {
			return nil
		}
		targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
		if targetPath == "" {
			return nil
		}
		if d.IsDir() {
			ensureDirNode(root, targetPath)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not supported: %s", current)
		}
		fileBytes, err := stageFlatUnixFSFile(ctx, root, casClient, current, targetPath)
		if err != nil {
			return err
		}
		files++
		bytesUploaded += fileBytes
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, bytesUploaded, nil
}

func stageFlatUnixFSFile(ctx context.Context, root *addNode, casClient addCASClient, localPath string, targetPath string) (int64, error) {
	targetPath = canonicalAddPath(targetPath)
	if targetPath == "" {
		return 0, fmt.Errorf("target path must not be empty")
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("not a regular file: %s", localPath)
	}
	node := ensureFileNode(root, targetPath)
	if info.Size() <= addFixedChunkSize {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", localPath, err)
		}
		blockCID, err := casClient.Put(ctx, data)
		if err != nil {
			return 0, fmt.Errorf("upload %s to CAS: %w", localPath, err)
		}
		node.Key = blockCID
		node.Chunks = nil
		return info.Size(), nil
	}
	chunks, err := uploadFlatChunks(ctx, casClient, localPath)
	if err != nil {
		return 0, err
	}
	node.Key = cid.Undef
	node.Chunks = chunks
	return info.Size(), nil
}

func uploadFlatChunks(ctx context.Context, casClient addCASClient, localPath string) ([]cid.Cid, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	chunks := make([]cid.Cid, 0)
	buf := make([]byte, addFixedChunkSize)
	for {
		n, readErr := io.ReadFull(f, buf)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("read %s: %w", localPath, readErr)
		}
		if n > 0 {
			chunkCID, err := casClient.Put(ctx, slices.Clone(buf[:n]))
			if err != nil {
				return nil, fmt.Errorf("upload chunk for %s: %w", localPath, err)
			}
			chunks = append(chunks, chunkCID)
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("empty chunk sequence for %s", localPath)
	}
	return chunks, nil
}

func flatUnixFSBatchEntries(ctx context.Context, casClient addCASClient, root *addNode) ([]httpapi.BucketUnixFSBatchEntry, error) {
	entries := make([]httpapi.BucketUnixFSBatchEntry, 0)
	var walk func(prefix string, node *addNode) error
	walk = func(prefix string, node *addNode) error {
		if node == nil {
			return nil
		}
		if prefix != "" {
			switch node.Kind {
			case "dir":
				manifestCID, err := putAddDirectoryManifest(ctx, casClient, node)
				if err != nil {
					return err
				}
				entries = append(entries, httpapi.BucketUnixFSBatchEntry{
					Path:   prefix,
					Target: manifestCID.String(),
				})
			case "file":
				entry := httpapi.BucketUnixFSBatchEntry{Path: prefix}
				if len(node.Chunks) > 0 {
					entry.Chunks = make([]string, len(node.Chunks))
					for i, chunk := range node.Chunks {
						entry.Chunks[i] = chunk.String()
					}
				} else {
					entry.Target = node.Key.String()
				}
				entries = append(entries, entry)
				return nil
			}
		}
		names := make([]string, 0, len(node.Children))
		for name := range node.Children {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			childPath := name
			if prefix != "" {
				childPath = path.Join(prefix, name)
			}
			if err := walk(childPath, node.Children[name]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("", root); err != nil {
		return nil, err
	}
	return entries, nil
}

func putAddDirectoryManifest(ctx context.Context, casClient addCASClient, node *addNode) (cid.Cid, error) {
	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		names = append(names, name)
	}
	payload, err := manifest.Normalize(&manifest.DirectoryManifest{Entries: names}).MarshalJSON()
	if err != nil {
		return cid.Undef, fmt.Errorf("marshal directory manifest: %w", err)
	}
	manifestCID, err := casClient.PutWithCodec(ctx, payload, codec.CodecMaltManifest)
	if err != nil {
		return cid.Undef, fmt.Errorf("upload directory manifest: %w", err)
	}
	return manifestCID, nil
}

func addDirectoryWithUnixFS(ctx context.Context, daemon *daemonclient.Client, bucketID string, item addMountedInput) (int, int64, string, error) {
	mountBase := canonicalAddPath(item.MountBase)
	if mountBase == "" {
		return 0, 0, "", fmt.Errorf("directory mount path must not be empty")
	}

	resp, err := daemon.AddBucketUnixFSDirectory(ctx, bucketID, mountBase)
	if err != nil {
		return 0, 0, "", err
	}
	lastRoot := resp.NewRoot
	var files int
	var bytesUploaded int64

	err = filepath.WalkDir(item.Input.AbsPath, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current != item.Input.AbsPath && d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not supported: %s", current)
		}

		rel, err := filepath.Rel(item.Input.AbsPath, current)
		if err != nil {
			return fmt.Errorf("compute relative path %q: %w", current, err)
		}
		if rel == "." {
			return nil
		}

		targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
		if targetPath == "" {
			return nil
		}

		if d.IsDir() {
			resp, err := daemon.AddBucketUnixFSDirectory(ctx, bucketID, targetPath)
			if err != nil {
				return err
			}
			lastRoot = resp.NewRoot
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not supported: %s", current)
		}

		bytesWritten, root, err := addFileWithUnixFS(ctx, daemon, bucketID, current, targetPath)
		if err != nil {
			return err
		}
		files++
		bytesUploaded += bytesWritten
		lastRoot = root
		return nil
	})
	if err != nil {
		return 0, 0, "", err
	}
	return files, bytesUploaded, lastRoot, nil
}

func addFileWithUnixFS(ctx context.Context, daemon *daemonclient.Client, bucketID string, localPath string, targetPath string) (int64, string, error) {
	targetPath = canonicalAddPath(targetPath)
	if targetPath == "" {
		return 0, "", fmt.Errorf("target path must not be empty")
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return 0, "", fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("not a regular file: %s", localPath)
	}

	data, err := os.ReadFile(localPath)
	if err != nil {
		return 0, "", fmt.Errorf("read %s: %w", localPath, err)
	}
	resp, err := daemon.AddBucketUnixFSFile(ctx, bucketID, targetPath, data)
	if err != nil {
		return 0, "", err
	}
	return info.Size(), resp.NewRoot, nil
}

func resolveAddBucketID(defaultBucketID string, flagBucketID string) (string, error) {
	if trimmed := strings.TrimSpace(flagBucketID); trimmed != "" {
		return trimmed, nil
	}
	if trimmed := strings.TrimSpace(defaultBucketID); trimmed != "" {
		return trimmed, nil
	}
	return "", fmt.Errorf("bucket id is required; pass --bucket or set client.default_bucket_id")
}

func ensureAddBucket(ctx context.Context, daemon *daemonclient.Client, bucketID string, autoCreate bool) (*httpapi.Bucket, bool, error) {
	meta, err := daemon.GetBucket(ctx, bucketID)
	if err == nil {
		return meta, false, nil
	}
	var apiErr *daemonclient.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 && autoCreate {
		created, createErr := daemon.CreateBucket(ctx, bucketID, "")
		return created, createErr == nil, createErr
	}
	return nil, false, err
}

func buildAddStagingTree(ctx context.Context, casClient addCASClient, daemon *daemonclient.Client, bucketID string, rawInputs []string, opts addBuildOptions) (*addBuildResult, error) {
	inputs, err := collectAddInputs(rawInputs)
	if err != nil {
		return nil, err
	}
	mounted, err := mountAddInputs(inputs, opts)
	if err != nil {
		return nil, err
	}

	root := newDirNode()
	var files int
	var bytesUploaded int64

	for _, item := range mounted {
		if item.Input.Info.IsDir() {
			dirFiles, dirBytes, err := stageDirectoryInput(ctx, root, casClient, daemon, bucketID, item)
			if err != nil {
				return nil, err
			}
			files += dirFiles
			bytesUploaded += dirBytes
			continue
		}
		fileBytes, err := stageSingleFile(ctx, root, casClient, daemon, bucketID, item.Input.AbsPath, item.MountBase)
		if err != nil {
			return nil, err
		}
		files++
		bytesUploaded += fileBytes
	}

	return &addBuildResult{
		Root:  root,
		Files: files,
		Bytes: bytesUploaded,
	}, nil
}

func collectAddInputs(rawInputs []string) ([]addInput, error) {
	out := make([]addInput, 0, len(rawInputs))
	for _, raw := range rawInputs {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", raw, err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", raw, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink is not supported: %s", raw)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("only regular files and directories are supported: %s", raw)
		}
		out = append(out, addInput{
			Original: raw,
			AbsPath:  abs,
			BaseName: filepath.Base(abs),
			Info:     info,
		})
	}
	return out, nil
}

func mountAddInputs(inputs []addInput, opts addBuildOptions) ([]addMountedInput, error) {
	prefix := canonicalAddPath(opts.Prefix)
	if opts.Wrap && len(inputs) > 1 && strings.TrimSpace(opts.WrapName) == "" {
		return nil, fmt.Errorf("--wrap-name is required when --wrap is used with multiple inputs")
	}
	if opts.Wrap && len(inputs) == 1 && inputs[0].Info.IsDir() {
		return nil, fmt.Errorf("single directory input does not support extra wrapping")
	}

	seen := make(map[string]struct{})
	out := make([]addMountedInput, 0, len(inputs))
	for _, in := range inputs {
		mount := in.BaseName
		if opts.Wrap {
			wrapName := strings.TrimSpace(opts.WrapName)
			if wrapName == "" {
				wrapName = in.BaseName
			}
			mount = path.Join(canonicalAddPath(wrapName), in.BaseName)
		}
		if prefix != "" {
			mount = path.Join(prefix, mount)
		}
		mount = canonicalAddPath(mount)
		if mount == "" {
			return nil, fmt.Errorf("invalid mount path for input %q", in.Original)
		}
		if _, ok := seen[mount]; ok {
			return nil, fmt.Errorf("duplicate mounted target path %q", mount)
		}
		seen[mount] = struct{}{}
		out = append(out, addMountedInput{
			Input:     in,
			MountBase: mount,
		})
	}
	return out, nil
}

func stageDirectoryInput(ctx context.Context, root *addNode, casClient addCASClient, daemon *daemonclient.Client, bucketID string, item addMountedInput) (int, int64, error) {
	mountBase := item.MountBase
	ensureDirNode(root, mountBase)

	var files int
	var bytesUploaded int64
	err := filepath.WalkDir(item.Input.AbsPath, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current != item.Input.AbsPath {
			if d.Type()&fs.ModeSymlink != 0 {
				return fmt.Errorf("symlink is not supported: %s", current)
			}
		}

		rel, err := filepath.Rel(item.Input.AbsPath, current)
		if err != nil {
			return fmt.Errorf("compute relative path %q: %w", current, err)
		}
		if rel == "." {
			return nil
		}
		targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
		if targetPath == "" {
			return nil
		}

		if d.IsDir() {
			ensureDirNode(root, targetPath)
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not supported: %s", current)
		}

		fileBytes, err := stageSingleFile(ctx, root, casClient, daemon, bucketID, current, targetPath)
		if err != nil {
			return err
		}
		files++
		bytesUploaded += fileBytes
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, bytesUploaded, nil
}

func stageSingleFile(ctx context.Context, root *addNode, casClient addCASClient, daemon *daemonclient.Client, bucketID string, localPath string, targetPath string) (int64, error) {
	targetPath = canonicalAddPath(targetPath)
	if targetPath == "" {
		return 0, fmt.Errorf("target path must not be empty")
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("not a regular file: %s", localPath)
	}

	var key cid.Cid
	if info.Size() <= addFixedChunkSize {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", localPath, err)
		}
		blockCID, err := casClient.Put(ctx, data)
		if err != nil {
			return 0, fmt.Errorf("upload %s to CAS: %w", localPath, err)
		}
		key = blockCID
	} else {
		listRoot, err := uploadAsList(ctx, casClient, daemon, bucketID, localPath)
		if err != nil {
			return 0, err
		}
		key = listRoot
	}

	if err := setFileNode(root, targetPath, key); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func uploadAsList(ctx context.Context, casClient addCASClient, daemon *daemonclient.Client, bucketID string, localPath string) (cid.Cid, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return cid.Undef, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	chunks := make([]string, 0)
	buf := make([]byte, addFixedChunkSize)
	for {
		n, readErr := io.ReadFull(f, buf)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return cid.Undef, fmt.Errorf("read %s: %w", localPath, readErr)
		}
		if n > 0 {
			chunkCID, err := casClient.Put(ctx, slices.Clone(buf[:n]))
			if err != nil {
				return cid.Undef, fmt.Errorf("upload chunk for %s: %w", localPath, err)
			}
			chunks = append(chunks, chunkCID.String())
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if len(chunks) == 0 {
		return cid.Undef, fmt.Errorf("empty chunk sequence for %s", localPath)
	}
	resp, err := daemon.CreateBucketList(ctx, bucketID, chunks, addFixedChunkSize)
	if err != nil {
		return cid.Undef, err
	}
	root, err := cid.Decode(resp.Root)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode list root CID: %w", err)
	}
	return root, nil
}

func ensureDirNode(root *addNode, p string) *addNode {
	root.Changed = true
	if p == "" {
		return root
	}
	segments := splitAddPath(p)
	cur := root
	for _, seg := range segments {
		child, ok := cur.Children[seg]
		if !ok {
			child = newDirNode()
			child.Changed = true
			cur.Children[seg] = child
		}
		if child.Kind != "dir" {
			child = newDirNode()
			child.Changed = true
			cur.Children[seg] = child
		}
		cur.Changed = true
		cur = child
	}
	return cur
}

func setFileNode(root *addNode, p string, key cid.Cid) error {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return fmt.Errorf("file path must not be empty")
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]

	if existing, ok := parent.Children[name]; ok {
		if existing.Kind == "file" && existing.Key.Equals(key) {
			return nil
		}
	}
	parent.Children[name] = &addNode{
		Kind:        "file",
		Key:         key,
		StorageKind: storageKindFromCID(key),
		Changed:     true,
	}
	parent.Changed = true
	return nil
}

func ensureFileNode(root *addNode, p string) *addNode {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return nil
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]
	node := &addNode{
		Kind:        "file",
		StorageKind: "raw",
		Changed:     true,
	}
	parent.Children[name] = node
	parent.Changed = true
	return node
}

func mergeAddNodes(existing *addNode, staged *addNode) *addNode {
	if staged == nil {
		return existing
	}
	if existing == nil {
		return staged
	}
	if staged.Kind == "file" {
		if existing.Kind == "file" && existing.Key.Equals(staged.Key) {
			return existing
		}
		return staged
	}
	if existing.Kind != "dir" {
		return staged
	}
	for name, child := range staged.Children {
		mergedChild := mergeAddNodes(existing.Children[name], child)
		if existing.Children[name] != mergedChild {
			existing.Changed = true
		}
		if mergedChild != nil && mergedChild.Changed {
			existing.Changed = true
		}
		existing.Children[name] = mergedChild
	}
	return existing
}

func loadExistingBucketTree(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, bucketID string, rootCID string) (*addNode, error) {
	rootStat, err := daemon.StatBucketPath(ctx, bucketID, "")
	if err != nil {
		return nil, err
	}
	if rootStat.Key != rootCID {
		rootCID = rootStat.Key
	}
	if rootStat.Kind != "dir" {
		return nil, fmt.Errorf("bucket root must be directory, got %q", rootStat.Kind)
	}
	return loadBucketDirRecursive(ctx, daemon, casClient, bucketID, "", rootStat)
}

func loadBucketDirRecursive(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, bucketID string, currentPath string, stat *httpapi.BucketStatResponse) (*addNode, error) {
	node := newDirNode()
	node.Changed = false
	node.StorageKind = stat.StorageKind
	keyCID, err := cid.Decode(stat.Key)
	if err != nil {
		return nil, fmt.Errorf("decode directory key %q: %w", stat.Key, err)
	}
	node.Key = keyCID

	if strings.TrimSpace(stat.Payload) == "" {
		return node, nil
	}
	payloadCID, err := cid.Decode(stat.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode directory payload %q: %w", stat.Payload, err)
	}
	raw, err := casClient.Get(ctx, payloadCID)
	if err != nil {
		return nil, fmt.Errorf("fetch directory manifest %s: %w", stat.Payload, err)
	}
	m, err := manifest.ParseDirectoryJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("parse directory manifest %s: %w", stat.Payload, err)
	}
	for _, childName := range m.Entries {
		childPath := childName
		if currentPath != "" {
			childPath = path.Join(currentPath, childName)
		}
		childStat, err := daemon.StatBucketPath(ctx, bucketID, childPath)
		if err != nil {
			return nil, err
		}
		switch childStat.Kind {
		case "dir":
			childDir, err := loadBucketDirRecursive(ctx, daemon, casClient, bucketID, childPath, childStat)
			if err != nil {
				return nil, err
			}
			node.Children[childName] = childDir
		case "file":
			childKey, err := cid.Decode(childStat.Key)
			if err != nil {
				return nil, fmt.Errorf("decode file key %q: %w", childStat.Key, err)
			}
			node.Children[childName] = &addNode{
				Kind:        "file",
				StorageKind: childStat.StorageKind,
				Key:         childKey,
				Changed:     false,
			}
		default:
			return nil, fmt.Errorf("unsupported child kind %q at %q", childStat.Kind, childPath)
		}
	}
	return node, nil
}

func materializeDirectory(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, bucketID string, node *addNode) (*addMaterializeResult, error) {
	if node == nil || node.Kind != "dir" {
		return nil, fmt.Errorf("materializeDirectory requires a directory node")
	}

	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		names = append(names, name)
	}
	slices.Sort(names)

	desc := make(map[string]cid.Cid)
	childKeys := make(map[string]cid.Cid, len(node.Children))
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		if child.Kind == "dir" {
			mat, err := materializeDirectory(ctx, daemon, casClient, bucketID, child)
			if err != nil {
				return nil, err
			}
			child.Key = mat.Key
			child.Changed = false
			childKeys[name] = mat.Key
			desc[name] = mat.Key
			for rel, childKey := range mat.Descendants {
				desc[path.Join(name, rel)] = childKey
			}
			continue
		}
		childKeys[name] = child.Key
		desc[name] = child.Key
	}

	if !node.Changed && node.Key.Defined() {
		return &addMaterializeResult{
			Key:         node.Key,
			ArcCount:    0,
			Descendants: desc,
		}, nil
	}

	payloadBytes, err := (&manifest.DirectoryManifest{Entries: names}).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal directory manifest: %w", err)
	}
	payloadCID, err := casClient.Put(ctx, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("upload directory manifest: %w", err)
	}

	bindings := make(map[string]string, 1+len(childKeys)+len(desc))
	bindings["@payload"] = payloadCID.String()
	for name, key := range childKeys {
		bindings[name] = key.String()
	}
	for rel, key := range desc {
		if !strings.Contains(rel, "/") {
			continue
		}
		bindings[rel] = key.String()
	}

	resp, err := daemon.CreateBucketMap(ctx, bucketID, bindings)
	if err != nil {
		return nil, err
	}
	rootCID, err := cid.Decode(resp.Root)
	if err != nil {
		return nil, fmt.Errorf("decode created map root: %w", err)
	}
	node.Key = rootCID
	node.Changed = false
	node.StorageKind = "map"
	return &addMaterializeResult{
		Key:         rootCID,
		ArcCount:    countDefinedBindings(bindings),
		Descendants: desc,
	}, nil
}

func countDefinedBindings(bindings map[string]string) int {
	count := 0
	for _, v := range bindings {
		if strings.TrimSpace(v) != "" {
			count++
		}
	}
	return count
}

func splitAddPath(p string) []string {
	clean := canonicalAddPath(p)
	if clean == "" {
		return nil
	}
	parts := strings.Split(clean, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

func canonicalAddPath(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func storageKindFromCID(c cid.Cid) string {
	if !c.Defined() {
		return ""
	}
	codec := c.Prefix().Codec
	switch codec {
	case 0x55:
		return "raw"
	case 0x300002, 0x300004:
		return "list"
	case 0x300001, 0x300003:
		return "map"
	default:
		return ""
	}
}
