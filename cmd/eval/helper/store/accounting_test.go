package store_test

import (
	"context"
	"errors"
	"testing"

	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/storage/cas"
	"github.com/dewebprotocol/malt/storage/kv"
	cid "github.com/ipfs/go-cid"
)

func TestIsolatedSystemsCountIdenticalCASBlocksIndependently(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })

	first, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem first: %v", err)
	}
	second, err := factory.NewSystem(ctx, "merkledag")
	if err != nil {
		t.Fatalf("NewSystem second: %v", err)
	}

	if _, err := first.CAS.Put(ctx, []byte("same payload")); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if _, err := second.CAS.Put(ctx, []byte("same payload")); err != nil {
		t.Fatalf("second put: %v", err)
	}

	firstCAS := first.Meter.Snapshot().Categories[evalstore.CategoryCASPayload]
	secondCAS := second.Meter.Snapshot().Categories[evalstore.CategoryCASPayload]
	if firstCAS.NewObjectCount != 1 || secondCAS.NewObjectCount != 1 {
		t.Fatalf("isolated new objects = %d/%d, want 1/1", firstCAS.NewObjectCount, secondCAS.NewObjectCount)
	}
	if firstCAS.NewPersistedBytes != uint64(len("same payload")) || secondCAS.NewPersistedBytes != uint64(len("same payload")) {
		t.Fatalf("isolated new bytes = %d/%d, want payload bytes for both", firstCAS.NewPersistedBytes, secondCAS.NewPersistedBytes)
	}
}

func TestSharedSystemsExposeCrossSystemCASDedupAsDiagnosticMode(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeShared,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })

	first, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem first: %v", err)
	}
	second, err := factory.NewSystem(ctx, "merkledag")
	if err != nil {
		t.Fatalf("NewSystem second: %v", err)
	}

	if _, err := first.CAS.Put(ctx, []byte("same payload")); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if _, err := second.CAS.Put(ctx, []byte("same payload")); err != nil {
		t.Fatalf("second put: %v", err)
	}

	firstCAS := first.Meter.Snapshot().Categories[evalstore.CategoryCASPayload]
	secondCAS := second.Meter.Snapshot().Categories[evalstore.CategoryCASPayload]
	if firstCAS.NewObjectCount != 1 {
		t.Fatalf("first shared new objects = %d, want 1", firstCAS.NewObjectCount)
	}
	if secondCAS.AttemptedPutCount != 1 || secondCAS.NewObjectCount != 0 {
		t.Fatalf("second shared attempted/new objects = %d/%d, want 1/0", secondCAS.AttemptedPutCount, secondCAS.NewObjectCount)
	}
}

func TestMeteredKVCountsEveryChangedRecord(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	system, err := factory.NewSystem(ctx, "maltflat")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}

	if err := system.StateKV.Put(ctx, []byte("root"), []byte("v1")); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	if err := system.StateKV.Put(ctx, []byte("root"), []byte("v2")); err != nil {
		t.Fatalf("put v2: %v", err)
	}

	arctable := system.Meter.Snapshot().Categories[evalstore.CategoryArcTable]
	if arctable.NewObjectCount != 2 {
		t.Fatalf("changed records = %d, want 2", arctable.NewObjectCount)
	}
	if arctable.NewPersistedBytes != uint64(len("root")+len("v1")+len("root")+len("v2")) {
		t.Fatalf("changed bytes = %d, want key+value bytes for both writes", arctable.NewPersistedBytes)
	}
}

func TestMeteredCASPutBatchReportsStoredAlreadyPresentAndDuplicate(t *testing.T) {
	ctx := context.Background()
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })
	system, err := factory.NewSystem(ctx, "merkledag")
	if err != nil {
		t.Fatalf("NewSystem: %v", err)
	}

	results, err := system.CAS.PutBatch(ctx, []cas.Block{
		{Data: []byte("new")},
		{Data: []byte("new")},
	})
	if err != nil {
		t.Fatalf("first PutBatch: %v", err)
	}
	if results[0].Status != cas.PutStatusStored || results[1].Status != cas.PutStatusDuplicate {
		t.Fatalf("first statuses = %s/%s, want stored/duplicate", results[0].Status, results[1].Status)
	}

	results, err = system.CAS.PutBatch(ctx, []cas.Block{{Data: []byte("new")}})
	if err != nil {
		t.Fatalf("second PutBatch: %v", err)
	}
	if results[0].Status != cas.PutStatusAlreadyPresent {
		t.Fatalf("second status = %s, want already_present", results[0].Status)
	}
}

func TestMeteredCASGetPreservesUnderlyingKVErrors(t *testing.T) {
	boom := errors.New("disk failure")
	metered := evalstore.NewMeteredCAS(errorKV{getErr: boom}, evalstore.NewMeter())

	_, err := metered.Get(context.Background(), cid.Undef)
	if !errors.Is(err, boom) {
		t.Fatalf("Get error = %v, want to preserve %v", err, boom)
	}
}

type errorKV struct {
	getErr error
}

func (e errorKV) Get(context.Context, []byte) ([]byte, error) {
	return nil, e.getErr
}

func (e errorKV) BatchGet(context.Context, [][]byte) (map[string][]byte, error) {
	return nil, e.getErr
}

func (e errorKV) Put(context.Context, []byte, []byte) error {
	return e.getErr
}

func (e errorKV) Delete(context.Context, []byte) error {
	return e.getErr
}

func (e errorKV) Has(context.Context, []byte) (bool, error) {
	return false, e.getErr
}

func (e errorKV) NewIterator(context.Context, []byte, []byte) kvstore.Iterator {
	return nil
}

func (e errorKV) Batch() kvstore.Batch {
	return nil
}

func (e errorKV) Close() error {
	return nil
}

var _ kvstore.KVStore = errorKV{}
