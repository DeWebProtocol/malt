package rq3baseline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	merkledag "github.com/ipfs/boxo/ipld/merkledag"
	unixfs "github.com/ipfs/boxo/ipld/unixfs"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
)

const (
	categoryPayloadChunk    = "payload_chunk"
	categoryStructural      = "cas_structural_metadata"
	categoryMixed           = "mixed_payload_and_structural_metadata"
	statusNewlyPersisted    = "newly_persisted"
	statusAlreadyPresent    = "already_present"
	statusDuplicateInCommit = "duplicate_in_commit"
)

type accountingStore struct {
	mu sync.Mutex

	root    string
	initErr error
	closed  bool

	phaseAttempts map[string]struct{}
	events        []CASWriteEvent
	putNanos      int64
	getNanos      int64
	readObjects   int
	readBytes     int64
}

func newAccountingStore() *accountingStore {
	root, err := os.MkdirTemp("", "malt-rq3-logical-cas-")
	return &accountingStore{root: root, initErr: err}
}

func (s *accountingStore) beginPhase() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phaseAttempts = make(map[string]struct{})
	s.events = make([]CASWriteEvent, 0)
	s.putNanos = 0
	s.getNanos = 0
	s.readObjects = 0
	s.readBytes = 0
}

func (s *accountingStore) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.initErr != nil {
		err := s.initErr
		s.mu.Unlock()
		return nil, err
	}
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("evaluator CAS is closed")
	}
	data, err := os.ReadFile(s.blockPath(key))
	if err == nil {
		s.readObjects++
		s.readBytes += int64(len(data))
	}
	s.getNanos += time.Since(started).Nanoseconds()
	s.mu.Unlock()
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", clientcas.ErrNotFound, key)
	}
	if err != nil {
		return nil, err
	}
	computed, err := clientcas.CIDForBlock(clientcas.Block{Data: data, Codec: key.Type()})
	if err != nil || !computed.Equals(key) {
		return nil, fmt.Errorf("disk-backed evaluator CAS block does not match CID %s", key)
	}
	return data, nil
}

func (s *accountingStore) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	if codec != cid.Raw && codec != cid.DagProtobuf {
		return cid.Undef, fmt.Errorf("unsupported evaluator CAS codec %d", codec)
	}
	key, err := clientcas.CIDForBlock(clientcas.Block{Data: data, Codec: codec})
	if err != nil {
		return cid.Undef, err
	}
	category, payloadBytes, structuralBytes, err := classifyBlock(key, data, codec)
	if err != nil {
		return cid.Undef, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.putNanos += time.Since(started).Nanoseconds() }()
	if s.initErr != nil {
		return cid.Undef, s.initErr
	}
	if s.closed {
		return cid.Undef, fmt.Errorf("evaluator CAS is closed")
	}
	if s.phaseAttempts == nil {
		return cid.Undef, fmt.Errorf("CAS accounting phase is not active")
	}
	keyString := key.String()
	status := statusNewlyPersisted
	objectPath := s.blockPath(key)
	if existing, readErr := os.ReadFile(objectPath); readErr == nil {
		if !bytes.Equal(existing, data) {
			return cid.Undef, fmt.Errorf("CID collision for %s", key)
		}
		if _, attempted := s.phaseAttempts[keyString]; attempted {
			status = statusDuplicateInCommit
		} else {
			status = statusAlreadyPresent
		}
	} else if !os.IsNotExist(readErr) {
		return cid.Undef, readErr
	} else {
		if err := os.MkdirAll(filepath.Dir(objectPath), 0o700); err != nil {
			return cid.Undef, err
		}
		file, err := os.OpenFile(objectPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return cid.Undef, err
		}
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return cid.Undef, cleanupFailedPayloadFile(objectPath, writeErr, closeErr, os.Remove)
		}
	}
	s.phaseAttempts[keyString] = struct{}{}
	s.events = append(s.events, CASWriteEvent{
		Sequence:                len(s.events),
		CID:                     keyString,
		Codec:                   codec,
		Category:                category,
		Bytes:                   int64(len(data)),
		PayloadBytes:            payloadBytes,
		StructuralMetadataBytes: structuralBytes,
		Status:                  status,
	})
	return key, nil
}

func (s *accountingStore) blockPath(key cid.Cid) string {
	digest := sha256.Sum256(key.Bytes())
	name := hex.EncodeToString(digest[:])
	return filepath.Join(s.root, name[:2], name[2:4], name[4:])
}

func (s *accountingStore) close() error {
	s.mu.Lock()
	s.closed = true
	root := s.root
	s.mu.Unlock()
	if root == "" {
		return nil
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	s.mu.Lock()
	if s.root == root {
		s.root = ""
	}
	s.mu.Unlock()
	return nil
}

// retryCleanup gives one-shot and construction-failure paths a second owner
// attempt while close retains its path on failure. Active streams additionally
// keep their store handle for caller-driven retries.
func retryCleanup(close func() error) error {
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

func (s *accountingStore) finishPhase() (CASAccounting, int64, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	accounting := CASAccounting{
		Events: append([]CASWriteEvent{}, s.events...),
		Reads:  CASReadAccounting{Objects: s.readObjects, Bytes: s.readBytes},
	}
	for _, event := range accounting.Events {
		accumulateCategory(&accounting.Total, event, event.Bytes)
		if event.PayloadBytes > 0 || event.Category == categoryPayloadChunk {
			accumulateCategory(&accounting.PayloadChunks, event, event.PayloadBytes)
		}
		if event.StructuralMetadataBytes > 0 {
			accumulateCategory(&accounting.StructuralMetadata, event, event.StructuralMetadataBytes)
		}
	}
	return accounting, s.putNanos, s.getNanos
}

func accumulateCategory(summary *CategoryAccounting, event CASWriteEvent, bytes int64) {
	summary.AttemptedObjects++
	summary.AttemptedBytes += bytes
	switch event.Status {
	case statusNewlyPersisted:
		summary.NewlyPersistedObjects++
		summary.NewlyPersistedBytes += bytes
	case statusAlreadyPresent:
		summary.AlreadyPresentObjects++
		summary.AlreadyPresentBytes += bytes
	case statusDuplicateInCommit:
		summary.DuplicateObjects++
		summary.DuplicateBytes += bytes
	}
}

func classifyBlock(key cid.Cid, data []byte, codec uint64) (string, int64, int64, error) {
	if codec == cid.Raw {
		return categoryPayloadChunk, int64(len(data)), 0, nil
	}
	block, err := blocks.NewBlockWithCid(data, key)
	if err != nil {
		return "", 0, 0, fmt.Errorf("decode dag-pb accounting block %s: %w", key, err)
	}
	node, err := merkledag.DecodeProtobufBlock(block)
	if err != nil {
		return "", 0, 0, fmt.Errorf("decode dag-pb accounting node %s: %w", key, err)
	}
	protoNode, ok := node.(*merkledag.ProtoNode)
	if !ok {
		return "", 0, 0, fmt.Errorf("decoded dag-pb accounting node %s has type %T", key, node)
	}
	unixFSData, err := unixfs.FromBytes(protoNode.Data())
	if err != nil {
		return "", 0, 0, fmt.Errorf("decode UnixFS accounting data %s: %w", key, err)
	}
	payloadBytes := int64(0)
	switch unixFSData.GetType() {
	case unixfs.TFile, unixfs.TRaw:
		payloadBytes = int64(len(unixFSData.GetData()))
	case unixfs.TDirectory, unixfs.THAMTShard:
		// HAMT uses Data for its bitfield. It is authentication structure,
		// not application payload, so the entire block remains metadata.
	default:
		return "", 0, 0, &UnsupportedError{Gap: "symlink_and_special_file_mutation", Message: fmt.Sprintf("UnixFS accounting node %s has unsupported type %d", key, unixFSData.GetType())}
	}
	structuralBytes := int64(len(data)) - payloadBytes
	if structuralBytes < 0 {
		return "", 0, 0, fmt.Errorf("UnixFS payload accounting exceeds dag-pb block %s", key)
	}
	if payloadBytes > 0 {
		return categoryMixed, payloadBytes, structuralBytes, nil
	}
	return categoryStructural, 0, structuralBytes, nil
}
