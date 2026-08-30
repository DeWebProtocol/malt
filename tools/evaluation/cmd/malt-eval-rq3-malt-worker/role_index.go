package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const casRoleIndexProfile = "process-temporary-leveldb-bounded-unmeasured/v1"

// blockRoleIndex keeps the cross-category CAS invariant exact without retaining
// every historical CID in the worker heap. It is process-temporary evaluator
// state: the Gateway's observed PutBatch disposition remains the source of
// write accounting, and this index is never included in the measured FSKV.
type blockRoleIndex struct {
	directory string
	database  *leveldb.DB
}

func newBlockRoleIndex() (*blockRoleIndex, error) {
	directory, err := os.MkdirTemp("", "malt-rq3-cas-role-index-")
	if err != nil {
		return nil, err
	}
	database, err := leveldb.OpenFile(directory, &opt.Options{
		BlockCacheCapacity:     4 << 20,
		BlockCacheEvictRemoved: true,
		CompactionTableSize:    4 << 20,
		OpenFilesCacheCapacity: 16,
		WriteBuffer:            4 << 20,
		CompactionTotalSize:    32 << 20,
		NoSync:                 true,
	})
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	return &blockRoleIndex{directory: directory, database: database}, nil
}

func (index *blockRoleIndex) checkAndRecord(key, category string) error {
	if index == nil || index.database == nil || index.directory == "" || key == "" || category == "" {
		return errors.New("CAS role index is not open or received an empty key/category")
	}
	previous, err := index.database.Get([]byte(key), nil)
	switch {
	case err == nil:
		if string(previous) != category {
			return fmt.Errorf("CAS object %s crosses mutually exclusive categories %q and %q", key, string(previous), category)
		}
		return nil
	case errors.Is(err, leveldb.ErrNotFound):
		if err := index.database.Put([]byte(key), []byte(category), nil); err != nil {
			return fmt.Errorf("record CAS role for %s: %w", key, err)
		}
		return nil
	default:
		return fmt.Errorf("read CAS role for %s: %w", key, err)
	}
}

func (index *blockRoleIndex) close() error {
	if index == nil {
		return nil
	}
	directory := index.directory
	var closeErr error
	if index.database != nil {
		closeErr = index.database.Close()
		index.database = nil
	}
	var removeErr error
	if directory != "" {
		removeErr = os.RemoveAll(directory)
		if removeErr == nil {
			index.directory = ""
		}
	}
	return errors.Join(closeErr, removeErr)
}
