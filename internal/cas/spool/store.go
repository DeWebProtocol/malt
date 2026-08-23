// Package spool provides a short-lived, owner-local CAS for one publication
// transaction. It performs no network I/O; its Plan-scoped owner removes it
// after use and recovers crash leftovers under the Plan operation lock.
package spool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	cid "github.com/ipfs/go-cid"
)

const maxBlockBytes = 64 << 20

type Store struct {
	directory string
}

func Open(directory string) (*Store, error) {
	directory = filepath.Clean(directory)
	if directory == "." || directory == "" {
		return nil, fmt.Errorf("snapshot spool directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(absolute, "blocks"), 0o700); err != nil {
		return nil, err
	}
	return &Store{directory: absolute}, nil
}

func (s *Store) Put(ctx context.Context, body []byte) (cid.Cid, error) {
	return s.PutWithCodec(ctx, body, cid.Raw)
}

func (s *Store) PutWithCodec(ctx context.Context, body []byte, codec uint64) (cid.Cid, error) {
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	if s == nil || s.directory == "" || len(body) > maxBlockBytes {
		return cid.Undef, fmt.Errorf("snapshot spool block is invalid or exceeds %d bytes", maxBlockBytes)
	}
	key, err := clientcas.CIDForBlock(clientcas.Block{Data: body, Codec: codec})
	if err != nil {
		return cid.Undef, err
	}
	target := filepath.Join(s.directory, "blocks", key.String())
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return key, nil
	}
	if err != nil {
		return cid.Undef, err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return cid.Undef, err
	}
	if err := file.Close(); err != nil {
		return cid.Undef, err
	}
	ok = true
	return key, nil
}

func (s *Store) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.directory == "" || !key.Defined() {
		return nil, fmt.Errorf("snapshot spool block identity is invalid")
	}
	body, err := os.ReadFile(filepath.Join(s.directory, "blocks", key.String()))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBlockBytes {
		return nil, fmt.Errorf("snapshot spool block exceeds %d bytes", maxBlockBytes)
	}
	computed, err := clientcas.CIDForBlock(clientcas.Block{Data: body, Codec: key.Type()})
	if err != nil {
		return nil, err
	}
	if !computed.Equals(key) {
		return nil, fmt.Errorf("snapshot spool block bytes do not match CID %s", key)
	}
	return body, nil
}
