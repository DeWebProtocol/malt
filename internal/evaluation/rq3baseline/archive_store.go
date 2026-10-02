package rq3baseline

import (
	"bufio"
	"bytes"
	"container/list"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	cid "github.com/ipfs/go-cid"
	car "github.com/ipld/go-car/v2"
	carstorage "github.com/ipld/go-car/v2/storage"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

const ReplayStorageBackend = "segmented-carv1-leveldb/v1"

const (
	archiveSegmentBytes     = 1 << 30
	archiveCacheBytes       = 32 << 20
	archiveWriteBufferBytes = 8 << 20
	archiveReadHandles      = 8
	archiveIndexHandles     = 32
	archiveMaxSectionBytes  = MaximumJSONLRecordBytes
)

// ReplayStorage is a terminal, CAS-only observation after independent CAR/index
// reconciliation and index close. File sizes are logical lengths, not allocated
// sectors. Storage.Writer counters exclude LevelDB's internal CURRENT/LOG writes.
// Source checkout staging and evaluator output files are outside this scope.
type ReplayStorage struct {
	Schema                string   `json:"schema_version"`
	Backend               string   `json:"backend"`
	SegmentLimit          int64    `json:"segment_limit_bytes,string"`
	IndexCache            int64    `json:"index_block_cache_bytes,string"`
	IndexBuffer           int64    `json:"index_write_buffer_bytes,string"`
	IndexHandles          int      `json:"index_open_files_cache"`
	ReadHandles           int      `json:"car_read_handles"`
	Objects               int64    `json:"cas_objects,string"`
	BodyBytes             int64    `json:"cas_body_bytes,string"`
	CARFiles              int64    `json:"car_files,string"`
	CARBytes              int64    `json:"car_file_bytes,string"`
	FramingBytes          int64    `json:"car_framing_bytes,string"`
	CARWriteBytes         int64    `json:"car_write_bytes,string"`
	IndexFiles            int64    `json:"index_files,string"`
	IndexBytes            int64    `json:"index_file_bytes,string"`
	IndexWriterBytes      int64    `json:"index_storage_writer_bytes,string"`
	IndexWriterCalls      int64    `json:"index_storage_writer_calls,string"`
	IndexWriterComplete   bool     `json:"index_writer_coverage_complete"`
	IndexWriterExclusions []string `json:"index_writer_exclusions"`
	Entries               int64    `json:"cas_filesystem_entries,string"`
	Reconciled            bool     `json:"car_index_reconciled"`
}

type archiveLocation struct {
	segment        uint32
	offset, length uint64
}

func (p archiveLocation) encode() []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint32(b, p.segment)
	binary.LittleEndian.PutUint64(b[4:], p.offset)
	binary.LittleEndian.PutUint64(b[12:], p.length)
	return b
}

func decodeArchiveLocation(b []byte) (archiveLocation, error) {
	if len(b) != 20 {
		return archiveLocation{}, fmt.Errorf("invalid CAR index location")
	}
	p := archiveLocation{binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint64(b[4:]), binary.LittleEndian.Uint64(b[12:])}
	if p.segment == 0 || p.offset > uint64(^uint64(0)>>1) || p.length == 0 || p.length > archiveMaxSectionBytes+10 || p.offset+p.length < p.offset {
		return archiveLocation{}, fmt.Errorf("CAR index location exceeds bounds")
	}
	return p, nil
}

type archiveReader struct {
	segment uint32
	file    *os.File
}

// archiveCAS is owned by accountingStore.mu. Only LevelDB's metered storage is
// concurrently accessed. No in-memory per-object index is retained here.
type archiveCAS struct {
	root          string
	index         *leveldb.DB
	indexStorage  *meteredIndexStorage
	header        []byte
	segmentLimit  int64
	segments      uint32
	active        *os.File
	activeBytes   int64
	carWritten    int64
	readers       *list.List
	failed        error
	sealed        bool
	handlesClosed bool
	closeErr      error
}

func newArchiveCAS(root string, segmentLimit int64) (*archiveCAS, error) {
	// Use the standard writer only for an empty rootless CARv1 header. Blocks
	// are appended below; go-car's growing in-memory insertion index is unused.
	var header bytes.Buffer
	w, err := carstorage.NewWritable(&header, []cid.Cid{}, car.WriteAsCarV1(true))
	if err != nil {
		return nil, err
	}
	if err := w.Finalize(); err != nil {
		return nil, err
	}
	if segmentLimit <= int64(header.Len()) {
		return nil, fmt.Errorf("CAR segment limit too small")
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0o700); err != nil {
		return nil, err
	}
	fs, err := storage.OpenFile(filepath.Join(root, "index"), false)
	if err != nil {
		return nil, err
	}
	meter := &meteredIndexStorage{Storage: fs}
	db, err := leveldb.Open(meter, &opt.Options{
		ErrorIfExist: true, Compression: opt.NoCompression, Strict: opt.StrictAll,
		BlockCacheCapacity: archiveCacheBytes, WriteBuffer: archiveWriteBufferBytes,
		OpenFilesCacheCapacity: archiveIndexHandles, Filter: filter.NewBloomFilter(10),
	})
	if err != nil {
		return nil, errors.Join(err, fs.Close())
	}
	return &archiveCAS{root: root, index: db, indexStorage: meter, header: append([]byte(nil), header.Bytes()...), segmentLimit: segmentLimit, readers: list.New()}, nil
}

func (s *archiveCAS) path(segment uint32) string {
	return filepath.Join(s.root, "data", fmt.Sprintf("%08d.car", segment))
}

func (s *archiveCAS) ready() error {
	if s.failed != nil {
		return s.failed
	}
	if s.sealed || s.index == nil {
		return fmt.Errorf("replay CAR store is sealed or closed")
	}
	return nil
}

func (s *archiveCAS) get(key cid.Cid) ([]byte, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	value, err := s.index.Get(key.Bytes(), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	p, err := decodeArchiveLocation(value)
	if err != nil {
		return nil, err
	}
	if p.segment > s.segments || p.offset < uint64(len(s.header)) {
		return nil, fmt.Errorf("unknown CAR segment or header offset")
	}
	f, err := s.reader(p.segment)
	if err != nil {
		return nil, fmt.Errorf("indexed CAR segment is unavailable: %v", err)
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || p.offset > uint64(info.Size()) || p.length > uint64(info.Size())-p.offset {
		return nil, fmt.Errorf("CAR index points outside a regular segment")
	}
	frame := make([]byte, int(p.length))
	if _, err := f.ReadAt(frame, int64(p.offset)); err != nil {
		return nil, err
	}
	length, n := binary.Uvarint(frame)
	if n <= 0 || length != uint64(len(frame)-n) || length > archiveMaxSectionBytes {
		return nil, fmt.Errorf("invalid CAR block framing")
	}
	if !bytes.Equal(binary.AppendUvarint(nil, length), frame[:n]) {
		return nil, fmt.Errorf("noncanonical CAR section length")
	}
	cidBytes, actual, err := cid.CidFromBytes(frame[n:])
	if err != nil || !actual.Equals(key) {
		return nil, fmt.Errorf("CAR index does not bind complete CID %s", key)
	}
	data := frame[n+cidBytes:]
	computed, err := key.Prefix().Sum(data)
	if err != nil || !computed.Equals(key) {
		return nil, fmt.Errorf("CAR block does not match CID %s", key)
	}
	return data, nil
}

func (s *archiveCAS) reader(segment uint32) (*os.File, error) {
	if segment == s.segments && s.active != nil {
		return s.active, nil
	}
	for e := s.readers.Front(); e != nil; e = e.Next() {
		r := e.Value.(archiveReader)
		if r.segment == segment {
			s.readers.MoveToFront(e)
			return r.file, nil
		}
	}
	if s.readers.Len() == archiveReadHandles {
		e := s.readers.Back()
		s.readers.Remove(e)
		if err := e.Value.(archiveReader).file.Close(); err != nil {
			s.failed = err
			return nil, err
		}
	}
	f, err := os.Open(s.path(segment))
	if err != nil {
		return nil, err
	}
	s.readers.PushFront(archiveReader{segment, f})
	return f, nil
}

func (s *archiveCAS) appendNew(key cid.Cid, data []byte) (err error) {
	if err := s.ready(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.failed = err
		}
	}()
	section := uint64(key.ByteLen() + len(data))
	if section > archiveMaxSectionBytes {
		return fmt.Errorf("CAR section exceeds replay record limit")
	}
	prefix := append(binary.AppendUvarint(nil, section), key.Bytes()...)
	frameBytes := int64(len(prefix) + len(data))
	if frameBytes+int64(len(s.header)) > s.segmentLimit {
		return fmt.Errorf("CAR block exceeds segment limit")
	}
	if s.active == nil || s.activeBytes+frameBytes > s.segmentLimit {
		if s.active != nil {
			f := s.active
			s.active = nil
			if err := f.Close(); err != nil {
				return err
			}
		}
		if s.segments == ^uint32(0) {
			return fmt.Errorf("too many CAR segments")
		}
		s.segments++
		s.active, err = os.OpenFile(s.path(s.segments), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		s.activeBytes = 0
		if err := s.write(s.header); err != nil {
			return err
		}
	}
	p := archiveLocation{s.segments, uint64(s.activeBytes), uint64(frameBytes)}
	if err := s.write(prefix); err != nil {
		return err
	}
	if err := s.write(data); err != nil {
		return err
	}
	// Publication follows the complete frame. Any failure poisons this run;
	// partially written data are never accepted as a resumable measurement.
	return s.index.Put(key.Bytes(), p.encode(), &opt.WriteOptions{Sync: false})
}

func (s *archiveCAS) write(data []byte) error {
	n, err := s.active.Write(data)
	s.activeBytes += int64(n)
	s.carWritten += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

type countedReader struct {
	io.Reader
	n uint64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.n += uint64(n)
	return n, err
}

// inventory scans every physical CAR frame with go-car's CID validation, then
// binds its exact location to the disk index. Duplicate frames, orphan frames,
// missing/extra index entries and truncated sections fail reconciliation.
func (s *archiveCAS) inventory() (report ReplayStorage, err error) {
	if err := s.ready(); err != nil {
		return report, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "data"))
	if err != nil {
		return report, err
	}
	if len(entries) != int(s.segments) {
		return report, fmt.Errorf("unexpected CAR segment set")
	}
	for segment := uint32(1); segment <= s.segments; segment++ {
		if err := s.scanSegment(segment, &report); err != nil {
			return report, err
		}
	}
	iter := s.index.NewIterator(nil, &opt.ReadOptions{DontFillCache: true, Strict: opt.StrictAll})
	var indexed int64
	for iter.Next() {
		indexed++
	}
	err = iter.Error()
	iter.Release()
	if err != nil {
		return report, err
	}
	if indexed != report.Objects {
		return report, fmt.Errorf("CAR/index object count mismatch")
	}
	if report.CARBytes != s.carWritten {
		return report, fmt.Errorf("CAR retained bytes differ from completed writes")
	}
	report.Schema, report.Backend = "malt-replay-cas-storage/v1", ReplayStorageBackend
	report.SegmentLimit, report.IndexCache, report.IndexBuffer = s.segmentLimit, archiveCacheBytes, archiveWriteBufferBytes
	report.ReadHandles, report.IndexHandles = archiveReadHandles, archiveIndexHandles
	report.FramingBytes = report.CARBytes - report.BodyBytes
	report.CARWriteBytes = s.carWritten
	report.Reconciled = true
	return report, nil
}

func (s *archiveCAS) scanSegment(segment uint32, report *ReplayStorage) (returnErr error) {
	f, err := os.Open(s.path(segment))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > s.segmentLimit {
		return fmt.Errorf("invalid CAR segment")
	}
	reader := &countedReader{Reader: bufio.NewReaderSize(f, 256<<10)}
	blocks, err := car.NewBlockReader(reader, car.MaxAllowedSectionSize(archiveMaxSectionBytes))
	if err != nil {
		return err
	}
	if blocks.Version != 1 || len(blocks.Roots) != 0 || reader.n != uint64(len(s.header)) {
		return fmt.Errorf("invalid replay CAR header")
	}
	for {
		offset := reader.n
		block, err := blocks.Next()
		if errors.Is(err, io.EOF) {
			if reader.n != offset || reader.n != uint64(info.Size()) {
				return fmt.Errorf("truncated CAR frame")
			}
			break
		}
		if err != nil {
			return err
		}
		value, err := s.index.Get(block.Cid().Bytes(), &opt.ReadOptions{DontFillCache: true})
		if err != nil {
			return fmt.Errorf("CAR frame lacks index entry: %w", err)
		}
		want := archiveLocation{segment, offset, reader.n - offset}
		if !bytes.Equal(value, want.encode()) {
			return fmt.Errorf("CAR frame/index location mismatch")
		}
		report.Objects++
		report.BodyBytes += int64(len(block.RawData()))
	}
	report.CARFiles++
	report.CARBytes += info.Size()
	return nil
}

func (s *archiveCAS) finish() (ReplayStorage, error) {
	report, err := s.inventory()
	if err != nil {
		return report, err
	}
	if err := s.closeHandles(); err != nil {
		return report, err
	}
	report.IndexWriterBytes, report.IndexWriterCalls = s.indexStorage.snapshot()
	report.IndexWriterExclusions = []string{"LevelDB CURRENT metadata", "LevelDB diagnostic LOG"}
	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		report.Entries++
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular replay CAS entry")
		}
		if filepath.Dir(path) == filepath.Join(s.root, "index") {
			report.IndexFiles++
			report.IndexBytes += info.Size()
		}
		return nil
	})
	return report, err
}

func (s *archiveCAS) closeHandles() error {
	if s.handlesClosed {
		return s.closeErr
	}
	s.handlesClosed = true
	s.sealed = true
	var errs []error
	if s.active != nil {
		f := s.active
		s.active = nil
		errs = append(errs, f.Close())
	}
	for e := s.readers.Front(); e != nil; e = e.Next() {
		errs = append(errs, e.Value.(archiveReader).file.Close())
	}
	s.readers.Init()
	if s.index != nil {
		db := s.index
		s.index = nil
		errs = append(errs, db.Close())
	}
	if s.indexStorage != nil {
		errs = append(errs, s.indexStorage.Close())
	}
	s.closeErr = errors.Join(errs...)
	return s.closeErr
}
