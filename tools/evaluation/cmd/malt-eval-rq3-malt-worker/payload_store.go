package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/transport"
	cid "github.com/ipfs/go-cid"
)

type logicalPayloadStore struct {
	directory string
	parent    *logicalPayloadStore
	refs      map[string]uint64
}

func newLogicalPayloadStore() (*logicalPayloadStore, error) {
	directory, err := os.MkdirTemp("", "malt-rq3-flat-payloads-")
	if err != nil {
		return nil, err
	}
	return &logicalPayloadStore{directory: directory, refs: make(map[string]uint64)}, nil
}

// newLogicalPayloadOverlay gives chunk prevalidation a private, bounded spill
// directory while allowing it to read the already-live measured checkout. This
// prevents prevalidation from warming or permanently growing the measured
// payload store.
func newLogicalPayloadOverlay(parent *logicalPayloadStore) (*logicalPayloadStore, error) {
	store, err := newLogicalPayloadStore()
	if err != nil {
		return nil, err
	}
	store.parent = parent
	return store, nil
}

func (s *logicalPayloadStore) retain(data []byte, mode uint32, digest string) (logicalFile, error) {
	payload, err := clientcas.CIDForBlock(transport.Block{Codec: cid.Raw, Data: data})
	if err != nil {
		return logicalFile{}, err
	}
	if s == nil {
		return logicalFile{data: append([]byte(nil), data...), mode: mode, digest: digest, size: int64(len(data)), payload: payload}, nil
	}
	if s.directory == "" {
		return logicalFile{}, fmt.Errorf("MALT-flat logical payload store is closed")
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != digest {
		return logicalFile{}, fmt.Errorf("MALT-flat logical payload %q has a mismatched digest", digest)
	}
	if s.refs[digest] > 0 {
		s.refs[digest]++
		return logicalFile{mode: mode, digest: digest, size: int64(len(data)), payload: payload}, nil
	}
	path := filepath.Join(s.directory, digest)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return logicalFile{}, readErr
		}
		if !bytes.Equal(existing, data) {
			return logicalFile{}, fmt.Errorf("MALT-flat logical payload %q differs from its existing content", digest)
		}
		s.refs[digest]++
		return logicalFile{mode: mode, digest: digest, size: int64(len(data)), payload: payload}, nil
	}
	if err != nil {
		return logicalFile{}, err
	}
	if _, err := file.Write(data); err != nil {
		return logicalFile{}, cleanupFailedPayloadFile(path, err, file.Close(), os.Remove)
	}
	if err := file.Close(); err != nil {
		return logicalFile{}, cleanupFailedPayloadFile(path, err, nil, os.Remove)
	}
	s.refs[digest]++
	return logicalFile{mode: mode, digest: digest, size: int64(len(data)), payload: payload}, nil
}

func cleanupFailedPayloadFile(path string, primary, closeErr error, remove func(string) error) error {
	removeErr := remove(path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	return errors.Join(primary, closeErr, removeErr)
}

func (s *logicalPayloadStore) read(file logicalFile) ([]byte, error) {
	if file.data != nil {
		return file.data, nil
	}
	if s == nil || s.directory == "" {
		return nil, fmt.Errorf("MALT-flat logical payload store is closed")
	}
	data, err := os.ReadFile(filepath.Join(s.directory, file.digest))
	if os.IsNotExist(err) && s.parent != nil {
		return s.parent.read(file)
	}
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != file.size || hex.EncodeToString(digest[:]) != file.digest {
		return nil, fmt.Errorf("MALT-flat logical payload %q failed validation", file.digest)
	}
	return data, nil
}

// reconcile retains exactly the immutable payloads referenced by the supplied
// live checkout states. Intermediate and replaced payloads are removed between
// commits, bounding disk use by the current checkout rather than full history.
func (s *logicalPayloadStore) reconcile(states ...map[string]logicalFile) error {
	if s == nil || s.directory == "" {
		return fmt.Errorf("MALT-flat logical payload store is closed")
	}
	live := make(map[string]uint64)
	for _, state := range states {
		for _, file := range state {
			if file.digest == "" {
				return fmt.Errorf("MALT-flat live payload has an empty digest")
			}
			live[file.digest]++
		}
	}
	var reconcileErr error
	for digest := range s.refs {
		if live[digest] != 0 {
			continue
		}
		if err := os.Remove(filepath.Join(s.directory, digest)); err != nil && !os.IsNotExist(err) {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("remove unreferenced MALT-flat logical payload %q: %w", digest, err))
			continue
		}
		delete(s.refs, digest)
	}
	for digest, count := range live {
		if _, err := os.Stat(filepath.Join(s.directory, digest)); err != nil {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("stat live MALT-flat logical payload %q: %w", digest, err))
			continue
		}
		s.refs[digest] = count
	}
	return reconcileErr
}

func (s *logicalPayloadStore) close() error {
	if s == nil || s.directory == "" {
		return nil
	}
	directory := s.directory
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	s.directory = ""
	s.refs = nil
	s.parent = nil
	return nil
}

func retryMALTCleanup(close func() error) error {
	if close == nil {
		return nil
	}
	if err := close(); err != nil {
		if retryErr := close(); retryErr != nil {
			return errors.Join(err, retryErr)
		}
	}
	return nil
}
