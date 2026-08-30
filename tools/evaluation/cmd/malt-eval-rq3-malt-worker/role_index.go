package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

const casRoleIndexProfile = "process-temporary-leveldb-bounded-memory-unmeasured/v1"

type blockRole struct {
	key      string
	category string
}

// blockRoleIndex keeps the cross-category CAS invariant exact with bounded
// block-cache and memtable memory instead of retaining every historical CID in
// the worker heap. Its process-temporary disk grows O(unique CIDs) and is
// removed on close. The Gateway's observed PutBatch disposition remains the
// source of write accounting, and this index is never included in the measured
// FSKV.
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
	return index.checkAndRecordBatch([]blockRole{{key: key, category: category}})
}

func (index *blockRoleIndex) checkAndRecordBatch(roles []blockRole) error {
	if index == nil || index.database == nil || index.directory == "" {
		return errors.New("CAS role index is not open")
	}
	if len(roles) == 0 {
		return nil
	}
	pending := make(map[string]string, len(roles))
	batch := new(leveldb.Batch)
	writes := 0
	for _, role := range roles {
		if role.key == "" || role.category == "" {
			return errors.New("CAS role index received an empty key/category")
		}
		if previous, ok := pending[role.key]; ok {
			if previous != role.category {
				return fmt.Errorf("CAS object %s crosses mutually exclusive categories %q and %q", role.key, previous, role.category)
			}
			continue
		}
		previous, err := index.database.Get([]byte(role.key), nil)
		switch {
		case err == nil:
			if string(previous) != role.category {
				return fmt.Errorf("CAS object %s crosses mutually exclusive categories %q and %q", role.key, string(previous), role.category)
			}
			pending[role.key] = role.category
		case errors.Is(err, leveldb.ErrNotFound):
			pending[role.key] = role.category
			batch.Put([]byte(role.key), []byte(role.category))
			writes++
		default:
			return fmt.Errorf("read CAS role for %s: %w", role.key, err)
		}
	}
	if writes == 0 {
		return nil
	}
	if err := index.database.Write(batch, nil); err != nil {
		return fmt.Errorf("record CAS role batch: %w", err)
	}
	return nil
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
