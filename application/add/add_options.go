package add

import (
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
)

const addFixedChunkSize = unixfsmodel.DefaultChunkSize

const (
	addTargetMALT      = "malt"
	addTargetMerkleDAG = "merkle-dag"

	addModelUnixFS = "unixfs"

	addLayoutHybrid   = "hybrid"
	addLayoutHybridV1 = "hybrid-v1"
	addLayoutFlatV1   = "flat-v1"

	addFileLayoutBalanced = "balanced"
	addFileLayoutTrickle  = "trickle"

	addDirLayoutBasic    = "basic"
	addDirLayoutHAMT     = "hamt"
	addDirLayoutAdaptive = "adaptive"
)

type addBuildOptions struct {
	Prefix     string
	Wrap       bool
	WrapName   string
	Target     string
	Model      string
	Layout     string
	FileLayout string
	DirLayout  string
	Ignore     addIgnoreOptions
}

func normalizeAddBuildOptions(opts addBuildOptions) (addBuildOptions, error) {
	opts.Target = normalizeAddToken(opts.Target)
	opts.Model = normalizeAddToken(opts.Model)
	opts.Layout = normalizeAddToken(opts.Layout)
	opts.FileLayout = normalizeAddToken(opts.FileLayout)
	opts.DirLayout = normalizeAddToken(opts.DirLayout)
	if opts.Target == "" {
		opts.Target = addTargetMALT
	}
	if opts.Target == "merkledag" || opts.Target == "merkle_dag" {
		opts.Target = addTargetMerkleDAG
	}
	if opts.Model == "" {
		opts.Model = addModelUnixFS
	}
	if opts.Model != addModelUnixFS {
		return opts, fmt.Errorf("unsupported add model %q", opts.Model)
	}
	switch opts.Target {
	case addTargetMALT:
		if opts.Layout == "" {
			opts.Layout = addLayoutHybrid
		}
		if opts.Layout != addLayoutHybrid && opts.Layout != addLayoutHybridV1 && opts.Layout != addLayoutFlatV1 {
			return opts, fmt.Errorf(
				"unsupported malt unixfs layout %q: supported layouts are %q and %q",
				opts.Layout,
				addLayoutFlatV1,
				addLayoutHybridV1,
			)
		}
		if opts.FileLayout != "" || opts.DirLayout != "" {
			return opts, fmt.Errorf("--file-layout and --dir-layout are only supported with --target merkle-dag")
		}
	case addTargetMerkleDAG:
		if opts.Layout != "" {
			return opts, fmt.Errorf("--layout is only supported with --target malt; use --file-layout and --dir-layout for merkle-dag")
		}
		if opts.FileLayout == "" {
			opts.FileLayout = addFileLayoutBalanced
		}
		if opts.DirLayout == "" {
			opts.DirLayout = addDirLayoutAdaptive
		}
		if opts.FileLayout != addFileLayoutBalanced && opts.FileLayout != addFileLayoutTrickle {
			return opts, fmt.Errorf("unsupported merkle-dag unixfs file layout %q", opts.FileLayout)
		}
		if opts.DirLayout != addDirLayoutBasic && opts.DirLayout != addDirLayoutHAMT && opts.DirLayout != addDirLayoutAdaptive {
			return opts, fmt.Errorf("unsupported merkle-dag unixfs directory layout %q", opts.DirLayout)
		}
	default:
		return opts, fmt.Errorf("unsupported add target %q", opts.Target)
	}
	return opts, nil
}

func unixFSLayoutKind(raw string) unixfs.LayoutKind {
	if raw == addLayoutFlatV1 {
		return unixfs.LayoutFlatV1
	}
	return unixfs.LayoutHybridV1
}

func normalizeAddToken(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
