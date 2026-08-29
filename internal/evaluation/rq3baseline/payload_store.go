package rq3baseline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// payloadStore keeps live checkout content out of the worker heap. Files are
// immutable and addressed by the already-validated payload SHA-256.
type payloadStore struct {
	directory string
	refs      map[string]uint64
}

func newPayloadStore() (*payloadStore, error) {
	directory, err := os.MkdirTemp("", "malt-rq3-payloads-")
	if err != nil {
		return nil, err
	}
	return &payloadStore{directory: directory, refs: make(map[string]uint64)}, nil
}

func (s *payloadStore) put(digest string, data []byte) error {
	if s == nil || s.directory == "" {
		return fmt.Errorf("RQ3 payload store is closed")
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != digest {
		return fmt.Errorf("RQ3 payload store object %q has a mismatched digest", digest)
	}
	if s.refs[digest] > 0 {
		s.refs[digest]++
		return nil
	}
	path := filepath.Join(s.directory, digest)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !bytes.Equal(existing, data) {
			return fmt.Errorf("RQ3 payload store object %q differs from its existing content", digest)
		}
		s.refs[digest]++
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return cleanupFailedPayloadFile(path, err, file.Close(), os.Remove)
	}
	if err := file.Close(); err != nil {
		return cleanupFailedPayloadFile(path, err, nil, os.Remove)
	}
	s.refs[digest]++
	return nil
}

func cleanupFailedPayloadFile(path string, primary, closeErr error, remove func(string) error) error {
	removeErr := remove(path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	return errors.Join(primary, closeErr, removeErr)
}

func (s *payloadStore) read(file logicalFile) ([]byte, error) {
	if file.data != nil {
		return file.data, nil
	}
	if s == nil || s.directory == "" {
		return nil, fmt.Errorf("RQ3 payload store is closed")
	}
	data, err := os.ReadFile(filepath.Join(s.directory, file.hash))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if len(data) != file.size || hex.EncodeToString(digest[:]) != file.hash {
		return nil, fmt.Errorf("RQ3 payload store object %q failed validation", file.hash)
	}
	return data, nil
}

// reconcile retains exactly the immutable payloads referenced by the supplied
// live semantic and execution states. Replaced and deleted content is removed
// between chunks so disk use follows the live checkout rather than history.
func (s *payloadStore) reconcile(states ...map[string]logicalFile) error {
	if s == nil || s.directory == "" {
		return fmt.Errorf("RQ3 payload store is closed")
	}
	live := make(map[string]uint64)
	for _, state := range states {
		for _, file := range state {
			if file.hash == "" {
				return fmt.Errorf("RQ3 live payload has an empty digest")
			}
			live[file.hash]++
		}
	}
	var reconcileErr error
	for digest := range s.refs {
		if live[digest] != 0 {
			continue
		}
		if err := os.Remove(filepath.Join(s.directory, digest)); err != nil && !os.IsNotExist(err) {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("remove unreferenced RQ3 payload %q: %w", digest, err))
			continue
		}
		delete(s.refs, digest)
	}
	for digest, count := range live {
		if _, err := os.Stat(filepath.Join(s.directory, digest)); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("stat live RQ3 payload %q: %w", digest, err))
			continue
		}
		s.refs[digest] = count
	}
	return reconcileErr
}

func (s *payloadStore) close() error {
	if s == nil || s.directory == "" {
		return nil
	}
	directory := s.directory
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	s.directory = ""
	s.refs = nil
	return nil
}

func retainLogicalFile(data []byte, mode uint32, digest string, store *payloadStore) (logicalFile, error) {
	if store == nil {
		return logicalFile{data: cloneBytes(data), mode: mode, hash: digest, size: len(data)}, nil
	}
	if err := store.put(digest, data); err != nil {
		return logicalFile{}, err
	}
	return logicalFile{mode: mode, hash: digest, size: len(data)}, nil
}
