package mount

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsDesiredAndPendingUnmountLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	created, err := store.PutDesired(spec)
	if err != nil || !created.Desired {
		t.Fatalf("PutDesired = %#v, %v", created, err)
	}
	if again, err := store.PutDesired(spec); err != nil || !again.Desired {
		t.Fatalf("idempotent PutDesired = %#v, %v", again, err)
	}
	other := testSpec(t, "other")
	other.Mountpoint = spec.Mountpoint
	if _, err := store.PutDesired(other); !errors.Is(err, ErrMountpointUse) {
		t.Fatalf("duplicate mountpoint error = %v", err)
	}
	changed := spec
	changed.Branch = "other"
	if _, err := store.PutDesired(changed); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("identity reuse error = %v", err)
	}

	pending, err := store.MarkPendingUnmount(spec.ID)
	if err != nil || pending.Desired {
		t.Fatalf("MarkPendingUnmount = %#v, %v", pending, err)
	}
	if _, err := store.PutDesired(spec); !errors.Is(err, ErrPendingUnmount) {
		t.Fatalf("PutDesired over pending unmount error = %v", err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := reopened.Get(spec.ID); err != nil || record.Desired {
		t.Fatalf("pending tombstone after restart = %#v, %v", record, err)
	}
	if err := reopened.DeleteUnmounted(spec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Get(spec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted mount still exists: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mount registry mode = %o, want owner-only", info.Mode().Perm())
	}
}

func TestStoreRejectsUnsafeSpecs(t *testing.T) {
	base := testSpec(t, "docs")
	tests := []Spec{base, base, base, base, base}
	tests[0].ID = "bad/id"
	tests[1].DatasetID = "bucket-\xff"
	tests[2].Mountpoint = "relative/path"
	tests[3].Mountpoint = string(filepath.Separator)
	tests[4].WritePolicy = "write_back"
	for index, spec := range tests {
		store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutDesired(spec); err == nil {
			t.Fatalf("unsafe spec %d was accepted: %#v", index, spec)
		}
	}
}

func TestStoreRejectsLossyUnicodeAndDuplicatePersistedMountpoints(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "invalid UTF-8", data: []byte{'{', '"', 0xff, '"', '}'}},
		{name: "lone surrogate", data: []byte(`{"version":1,"records":{"\ud800":{}}}`)},
		{name: "unknown field", data: []byte(`{"version":1,"records":{},"unexpected":true}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mounts.json")
			if err := os.WriteFile(path, test.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenStore(path); err == nil {
				t.Fatal("store accepted lossy persisted Unicode")
			}
		})
	}

	path := filepath.Join(t.TempDir(), "mounts.json")
	first := testSpec(t, "one")
	second := testSpec(t, "two")
	second.Mountpoint = first.Mountpoint
	now := time.Now().UTC()
	state := registryState{Version: registryVersion, Records: map[string]Record{
		first.ID:  {Spec: first, Desired: true, UpdatedAt: now},
		second.ID: {Spec: second, Desired: false, UpdatedAt: now},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "same mountpoint") {
		t.Fatalf("duplicate persisted mountpoint error = %v", err)
	}

	missingDesiredPath := filepath.Join(t.TempDir(), "mounts.json")
	valid := registryState{Version: registryVersion, Records: map[string]Record{
		first.ID: {Spec: first, Desired: true, UpdatedAt: now},
	}}
	data, err = json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	withoutDesired := bytes.Replace(data, []byte(`"desired":true,`), nil, 1)
	if bytes.Equal(withoutDesired, data) {
		t.Fatal("test fixture did not remove desired field")
	}
	if err := os.WriteFile(missingDesiredPath, withoutDesired, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(missingDesiredPath); err == nil || !strings.Contains(err.Error(), "missing required desired") {
		t.Fatalf("missing desired error = %v", err)
	}
}

func testSpec(t *testing.T, id string) Spec {
	t.Helper()
	return Spec{
		ID: id, DatasetID: "bucket-one", Branch: "main",
		Mountpoint: filepath.Join(t.TempDir(), "mnt"), TrustAlias: "docs",
		CachePolicy: CacheVerified, WritePolicy: WriteReadOnly,
		EncryptionEpoch: 2, ConflictPolicy: ConflictFailReadOnly,
	}
}
