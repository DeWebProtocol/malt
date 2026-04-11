package lineage

import (
	"context"

	"github.com/dewebprotocol/malt/core/kvstore"
)

// kvStoreAdapter adapts kvstore.KVStore to the lineage KVStore interface.
// The kvstore uses (ctx, []byte) -> ([]byte, error) while lineage uses
// (string) -> ([]byte, bool).
type kvStoreAdapter struct {
	kv kvstore.KVStore
}

// NewKVStoreAdapter wraps a kvstore.KVStore to implement lineage.KVStore.
func NewKVStoreAdapter(kv kvstore.KVStore) *kvStoreAdapter {
	return &kvStoreAdapter{kv: kv}
}

func (a *kvStoreAdapter) Get(key string) ([]byte, bool) {
	val, err := a.kv.Get(context.Background(), []byte(key))
	if err != nil {
		return nil, false
	}
	return val, true
}

func (a *kvStoreAdapter) Set(key string, value []byte) error {
	return a.kv.Put(context.Background(), []byte(key), value)
}

func (a *kvStoreAdapter) Delete(key string) error {
	return a.kv.Delete(context.Background(), []byte(key))
}

func (a *kvStoreAdapter) Keys() []string {
	var keys []string
	ctx := context.Background()
	iter := a.kv.NewIterator(ctx, nil, nil)
	defer iter.Close()
	for iter.Next() {
		keys = append(keys, string(iter.Key()))
	}
	return keys
}
