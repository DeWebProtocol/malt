package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
)

const writableLayoutStateVersion = 1

var ErrWritableLayoutChanged = errors.New("filesystem write-back layout differs from durable dataset state")

// writableLayoutState freezes the application projection profile independently
// from the upload identity. A retry may therefore reopen the same journal only
// with the exact layout that produced any request that could have reached an
// untrusted executor.
type writableLayoutState struct {
	Version      int                          `json:"version"`
	DatasetID    string                       `json:"dataset_id"`
	Branch       string                       `json:"branch"`
	LayoutPolicy filesystemmount.LayoutPolicy `json:"layout_policy"`
}

func ensureWritableLayoutState(path string, spec filesystemmount.Spec) error {
	expected := writableLayoutState{
		Version: writableLayoutStateVersion, DatasetID: spec.DatasetID,
		Branch: spec.Branch, LayoutPolicy: spec.LayoutPolicy,
	}
	current, err := readWritableLayoutState(path)
	if err == nil {
		if current != expected {
			return fmt.Errorf("%w: stored=%s/%s/%s requested=%s/%s/%s", ErrWritableLayoutChanged,
				current.DatasetID, current.Branch, current.LayoutPolicy,
				expected.DatasetID, expected.Branch, expected.LayoutPolicy)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeWritableLayoutState(path, expected)
}

func readWritableLayoutState(path string) (writableLayoutState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return writableLayoutState{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return writableLayoutState{}, fmt.Errorf("filesystem write-back layout state is not a bounded regular file")
	}
	if err := securefile.Secure(path); err != nil {
		return writableLayoutState{}, fmt.Errorf("secure filesystem write-back layout state: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return writableLayoutState{}, err
	}
	if err := strictjson.ValidateUnicode(data); err != nil {
		return writableLayoutState{}, fmt.Errorf("validate filesystem write-back layout state: %w", err)
	}
	if err := strictjson.RejectDuplicateKeys(data); err != nil {
		return writableLayoutState{}, fmt.Errorf("validate filesystem write-back layout state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state writableLayoutState
	if err := decoder.Decode(&state); err != nil {
		return writableLayoutState{}, fmt.Errorf("decode filesystem write-back layout state: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return writableLayoutState{}, fmt.Errorf("decode filesystem write-back layout state: %w", err)
	}
	if state.Version != writableLayoutStateVersion || state.DatasetID == "" || state.Branch == "" ||
		(state.LayoutPolicy != filesystemmount.LayoutFlatV1 && state.LayoutPolicy != filesystemmount.LayoutHybridV1) {
		return writableLayoutState{}, fmt.Errorf("filesystem write-back layout state is invalid")
	}
	return state, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeWritableLayoutState(path string, state writableLayoutState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".writeback-layout-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = tmp.Close()
		return err
	}
	written, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if written != len(data) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if err := securefile.Secure(path); err != nil {
		return err
	}
	return durablefile.SyncParent(path)
}
