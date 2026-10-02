package rq3baseline

import (
	"sync"

	"github.com/syndtr/goleveldb/leveldb/storage"
)

// Meter the public LevelDB storage writer, including WAL/table/manifest writes
// and compaction. CURRENT and diagnostic logging bypass this interface; their
// retained sizes are inventoried, and write coverage is explicitly incomplete.
type meteredIndexStorage struct {
	storage.Storage
	mu           sync.Mutex
	bytes, calls int64
}

func (s *meteredIndexStorage) Create(fd storage.FileDesc) (storage.Writer, error) {
	w, err := s.Storage.Create(fd)
	if err != nil {
		return nil, err
	}
	return &meteredIndexWriter{Writer: w, owner: s}, nil
}

func (s *meteredIndexStorage) snapshot() (int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes, s.calls
}

type meteredIndexWriter struct {
	storage.Writer
	owner *meteredIndexStorage
}

func (w *meteredIndexWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.owner.mu.Lock()
	w.owner.bytes += int64(n)
	w.owner.calls++
	w.owner.mu.Unlock()
	return n, err
}
