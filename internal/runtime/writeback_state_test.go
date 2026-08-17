package runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
)

func TestWritableLayoutStateRejectsDuplicateKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	data := []byte(`{"version":1,"dataset_id":"bucket","branch":"main","layout_policy":"flat-v1","layout_policy":"hybrid-v1"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWritableLayoutState(path); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
		t.Fatalf("duplicate layout key error=%v", err)
	}
}

func TestWritableLayoutStateRestoresOwnerPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "layout.json")
	state := writableLayoutState{
		Version: writableLayoutStateVersion, DatasetID: "bucket", Branch: "main",
		LayoutPolicy: filesystemmount.LayoutFlatV1,
	}
	if err := writeWritableLayoutState(path, state); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := readWritableLayoutState(path)
	if err != nil || loaded != state {
		t.Fatalf("loaded state=%#v err=%v", loaded, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("layout state permissions=%#o", info.Mode().Perm())
		}
	}
}
