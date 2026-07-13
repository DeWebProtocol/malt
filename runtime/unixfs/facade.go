package unixfs

import (
	"context"
	"fmt"
	"io"

	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/storage/cas"
	cid "github.com/ipfs/go-cid"
)

// Reader exposes the read and proof capabilities of the UnixFS adapter without
// granting access to payload writes.
type Reader interface {
	Resolve(ctx context.Context, root cid.Cid, path string) (*Resolution, error)
	Stat(ctx context.Context, root cid.Cid, path string) (*Stat, error)
	ReadFile(ctx context.Context, root cid.Cid, path string) ([]byte, error)
	ReadFileRange(ctx context.Context, root cid.Cid, path string, offset, length uint64) ([]byte, error)
	ListPayloadSize(ctx context.Context, root cid.Cid) (uint64, uint64, error)
	ReadListPayloadRange(ctx context.Context, root cid.Cid, offset, length uint64) ([]byte, error)
	AppendListPayloadRangeProof(ctx context.Context, pl *prooflist.ProofList, queriedPath string, root cid.Cid, offset, length uint64) error
	ListIndexStepsForFileRange(ctx context.Context, root cid.Cid, path string, offset, length uint64) ([]ListIndexStep, error)
}

// Writer exposes UnixFS mutation capabilities in addition to Reader. The
// writer facade owns an explicit CAS writer; callers using NewReader never
// receive this capability.
type Writer interface {
	Reader

	EmptyDirectory(ctx context.Context) (cid.Cid, error)
	AddDirectory(ctx context.Context, root cid.Cid, path string) (cid.Cid, error)
	AddFile(ctx context.Context, root cid.Cid, path string, data []byte) (cid.Cid, error)
	AddFileStream(ctx context.Context, root cid.Cid, path string, src io.Reader) (cid.Cid, error)
	RemovePath(ctx context.Context, root cid.Cid, path string) (cid.Cid, error)
	MutationPlanForPath(ctx context.Context, root cid.Cid, path string) (*MutationPlan, error)
	MutationPlanForRoot(ctx context.Context, baseRoot cid.Cid, root cid.Cid) (*MutationPlan, error)
}

// ReaderOptions configures a read-only UnixFS adapter facade.
type ReaderOptions struct {
	Namespace string
	ChunkSize int
	Map       mapping.Semantics
	List      list.Semantics
	Blocks    cas.Reader
}

// WriterOptions configures a read/write UnixFS adapter facade. Blocks is the
// read-side store and BlockWriter is the independently granted write
// capability. They may be implemented by the same CAS client.
type WriterOptions struct {
	Namespace   string
	ChunkSize   int
	Map         mapping.Semantics
	List        list.Semantics
	Blocks      cas.Reader
	BlockWriter cas.Writer
}

// NewReader creates a UnixFS facade that cannot write payload blocks.
func NewReader(opts ReaderOptions) (Reader, error) {
	adapter, err := newAdapter(
		opts.Namespace,
		opts.ChunkSize,
		opts.Map,
		opts.List,
		opts.Blocks,
	)
	if err != nil {
		return nil, err
	}
	return &readerFacade{adapter: adapter}, nil
}

// NewWriter creates a UnixFS facade with independently supplied CAS read and
// write capabilities.
func NewWriter(opts WriterOptions) (Writer, error) {
	adapter, err := newAdapter(
		opts.Namespace,
		opts.ChunkSize,
		opts.Map,
		opts.List,
		opts.Blocks,
	)
	if err != nil {
		return nil, err
	}
	if opts.BlockWriter == nil {
		return nil, fmt.Errorf("CAS writer is nil")
	}
	adapter.blockWriter = opts.BlockWriter
	reader := &readerFacade{adapter: adapter}
	return &writerFacade{readerFacade: reader, adapter: adapter}, nil
}

func newAdapter(namespace string, chunkSize int, maps mapping.Semantics, lists list.Semantics, blocks cas.Reader) (*Adapter, error) {
	if maps == nil {
		return nil, fmt.Errorf("map semantic is nil")
	}
	if lists == nil {
		return nil, fmt.Errorf("list semantic is nil")
	}
	if blocks == nil {
		return nil, fmt.Errorf("CAS reader is nil")
	}
	if namespace == "" {
		return nil, fmt.Errorf("namespace is empty")
	}
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize < 0 {
		return nil, fmt.Errorf("chunk size must be positive")
	}
	return &Adapter{
		namespace: namespace,
		chunkSize: chunkSize,
		maps:      maps,
		lists:     lists,
		blocks:    blocks,
	}, nil
}

type readerFacade struct {
	adapter *Adapter
}

func (r *readerFacade) Resolve(ctx context.Context, root cid.Cid, path string) (*Resolution, error) {
	return r.adapter.Resolve(ctx, root, path)
}

func (r *readerFacade) Stat(ctx context.Context, root cid.Cid, path string) (*Stat, error) {
	return r.adapter.Stat(ctx, root, path)
}

func (r *readerFacade) ReadFile(ctx context.Context, root cid.Cid, path string) ([]byte, error) {
	return r.adapter.ReadFile(ctx, root, path)
}

func (r *readerFacade) ReadFileRange(ctx context.Context, root cid.Cid, path string, offset, length uint64) ([]byte, error) {
	return r.adapter.ReadFileRange(ctx, root, path, offset, length)
}

func (r *readerFacade) ListPayloadSize(ctx context.Context, root cid.Cid) (uint64, uint64, error) {
	return r.adapter.ListPayloadSize(ctx, root)
}

func (r *readerFacade) ReadListPayloadRange(ctx context.Context, root cid.Cid, offset, length uint64) ([]byte, error) {
	return r.adapter.ReadListPayloadRange(ctx, root, offset, length)
}

func (r *readerFacade) AppendListPayloadRangeProof(ctx context.Context, pl *prooflist.ProofList, queriedPath string, root cid.Cid, offset, length uint64) error {
	return r.adapter.AppendListPayloadRangeProof(ctx, pl, queriedPath, root, offset, length)
}

func (r *readerFacade) ListIndexStepsForFileRange(ctx context.Context, root cid.Cid, path string, offset, length uint64) ([]ListIndexStep, error) {
	return r.adapter.ListIndexStepsForFileRange(ctx, root, path, offset, length)
}

type writerFacade struct {
	*readerFacade
	adapter *Adapter
}

func (w *writerFacade) EmptyDirectory(ctx context.Context) (cid.Cid, error) {
	return w.adapter.EmptyDirectory(ctx)
}

func (w *writerFacade) AddDirectory(ctx context.Context, root cid.Cid, path string) (cid.Cid, error) {
	return w.adapter.AddDirectory(ctx, root, path)
}

func (w *writerFacade) AddFile(ctx context.Context, root cid.Cid, path string, data []byte) (cid.Cid, error) {
	return w.adapter.AddFile(ctx, root, path, data)
}

func (w *writerFacade) AddFileStream(ctx context.Context, root cid.Cid, path string, src io.Reader) (cid.Cid, error) {
	return w.adapter.AddFileStream(ctx, root, path, src)
}

func (w *writerFacade) RemovePath(ctx context.Context, root cid.Cid, path string) (cid.Cid, error) {
	return w.adapter.RemovePath(ctx, root, path)
}

func (w *writerFacade) MutationPlanForPath(ctx context.Context, root cid.Cid, path string) (*MutationPlan, error) {
	return w.adapter.MutationPlanForPath(ctx, root, path)
}

func (w *writerFacade) MutationPlanForRoot(ctx context.Context, baseRoot cid.Cid, root cid.Cid) (*MutationPlan, error) {
	return w.adapter.MutationPlanForRoot(ctx, baseRoot, root)
}

var _ Reader = (*readerFacade)(nil)
var _ Writer = (*writerFacade)(nil)
