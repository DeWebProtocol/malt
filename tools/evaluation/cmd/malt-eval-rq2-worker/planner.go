package main

import (
	"crypto/sha256"
	"fmt"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2wire"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-client/transport"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type preparedPayloads struct {
	blocks []transport.Block
	cids   []cid.Cid
	scan   rq2wire.PhaseMeasurement
	chunk  rq2wire.PhaseMeasurement
	hash   rq2wire.PhaseMeasurement
}

type nativeWorkspace struct {
	root string
}

type changedFileRange struct {
	path   string
	offset int64
	length int
}

func newNativeWorkspace(fixture *rq2fixture.Fixture) (*nativeWorkspace, error) {
	if fixture == nil {
		return nil, fmt.Errorf("RQ2 source fixture is nil")
	}
	root, err := os.MkdirTemp("", "malt-rq2-native-workspace-")
	if err != nil {
		return nil, err
	}
	workspace := &nativeWorkspace{root: root}
	failed := true
	defer func() {
		if failed {
			_ = workspace.close()
		}
	}()
	for relative, data := range fixture.InitialSource() {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	failed = false
	return workspace, nil
}

func (w *nativeWorkspace) snapshot() (map[string][]byte, error) {
	if w == nil || w.root == "" {
		return nil, fmt.Errorf("native filesystem workspace is unavailable")
	}
	source := make(map[string][]byte)
	var total uint64
	err := filepath.WalkDir(w.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == w.root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("native source contains a non-regular file %q", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += uint64(len(data))
		if total > maxFixtureBytes {
			return fmt.Errorf("native source post-image exceeds %d bytes", maxFixtureBytes)
		}
		relative, err := filepath.Rel(w.root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, exists := source[relative]; exists {
			return fmt.Errorf("native source repeats path %q", relative)
		}
		source[relative] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return source, nil
}

func (w *nativeWorkspace) close() error {
	if w == nil || w.root == "" {
		return nil
	}
	root := w.root
	w.root = ""
	return os.RemoveAll(root)
}

// prepareNativeOperation mutates a real temporary filesystem tree and reads
// only the file bytes changed by that operation. Metadata-only operations
// execute a real filesystem rename/remove and explicitly report the payload
// scan/chunk/hash stages as not applicable.
func prepareNativeOperation(operation rq2fixture.Operation, fixture *rq2fixture.Fixture, workspace *nativeWorkspace, serial uint64) (preparedPayloads, error) {
	if workspace == nil || workspace.root == "" {
		return preparedPayloads{}, fmt.Errorf("native filesystem workspace is unavailable")
	}
	count, sizes, err := payloadShape(operation)
	if err != nil {
		return preparedPayloads{}, err
	}
	changed, err := workspace.apply(operation, fixture, serial, sizes)
	if err != nil {
		return preparedPayloads{}, fmt.Errorf("apply native filesystem operation %s: %w", operation.Name, err)
	}
	if count == 0 {
		if len(changed) != 0 {
			return preparedPayloads{}, fmt.Errorf("metadata-only operation unexpectedly changed payload bytes")
		}
		return preparedPayloads{scan: rq2wire.NotApplicablePhase(), chunk: rq2wire.NotApplicablePhase(), hash: rq2wire.NotApplicablePhase()}, nil
	}
	if len(changed) != count {
		return preparedPayloads{}, fmt.Errorf("filesystem operation changed %d payloads, want %d", len(changed), count)
	}
	scanStarted := time.Now()
	scanned := make([][]byte, len(changed))
	var changedBytes uint64
	for index, item := range changed {
		file, err := os.Open(item.path)
		if err != nil {
			return preparedPayloads{}, err
		}
		data := make([]byte, item.length)
		_, readErr := file.ReadAt(data, item.offset)
		closeErr := file.Close()
		if readErr != nil {
			return preparedPayloads{}, readErr
		}
		if closeErr != nil {
			return preparedPayloads{}, closeErr
		}
		scanned[index] = data
		changedBytes += uint64(len(data))
	}
	prepared := preparedPayloads{scan: rq2wire.ObservedPhase(durationNS(time.Since(scanStarted)), changedBytes, uint64(len(scanned)))}
	chunkStarted := time.Now()
	chunks := make([][]byte, len(scanned))
	for index := range scanned {
		chunks[index] = append([]byte(nil), scanned[index]...)
	}
	prepared.chunk = rq2wire.ObservedPhase(durationNS(time.Since(chunkStarted)), changedBytes, uint64(len(chunks)))
	hashStarted := time.Now()
	prepared.cids = make([]cid.Cid, len(chunks))
	prepared.blocks = make([]transport.Block, len(chunks))
	for index, chunk := range chunks {
		digest, err := mh.Sum(chunk, mh.SHA2_256, -1)
		if err != nil {
			return preparedPayloads{}, err
		}
		prepared.cids[index] = cid.NewCidV1(cid.Raw, digest)
		prepared.blocks[index] = transport.Block{Codec: cid.Raw, Data: chunk}
	}
	prepared.hash = rq2wire.ObservedPhase(durationNS(time.Since(hashStarted)), changedBytes, uint64(len(chunks)))
	return prepared, nil
}

func (w *nativeWorkspace) apply(operation rq2fixture.Operation, fixture *rq2fixture.Fixture, serial uint64, sizes []int) ([]changedFileRange, error) {
	payload := func(index int) ([]byte, error) {
		return fixture.Payload(operation.Name, serial, index, uint64(sizes[index]))
	}
	write := func(relative string, data []byte) (changedFileRange, error) {
		path := filepath.Join(w.root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return changedFileRange{}, err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return changedFileRange{}, err
		}
		return changedFileRange{path: path, length: len(data)}, nil
	}
	switch operation.Kind {
	case rq2fixture.KindListAppend:
		path := filepath.Join(w.root, filepath.FromSlash(operation.SourcePath))
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, err
		}
		stat, err := file.Stat()
		data, payloadErr := payload(0)
		if err == nil && payloadErr == nil {
			_, err = file.Write(data)
		}
		closeErr := file.Close()
		if payloadErr != nil {
			return nil, payloadErr
		}
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return []changedFileRange{{path: path, offset: stat.Size(), length: sizes[0]}}, nil
	case rq2fixture.KindListReplace:
		if operation.ListIndex == nil {
			return nil, fmt.Errorf("list replacement omits its index")
		}
		list, ok := fixture.List(operation.SourcePath)
		if !ok {
			return nil, fmt.Errorf("list replacement source is not declared")
		}
		path := filepath.Join(w.root, filepath.FromSlash(operation.SourcePath))
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		data, payloadErr := payload(0)
		if payloadErr == nil {
			_, err = file.WriteAt(data, int64(*operation.ListIndex*list.ChunkSize))
		}
		closeErr := file.Close()
		if payloadErr != nil {
			return nil, payloadErr
		}
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return []changedFileRange{{path: path, offset: int64(*operation.ListIndex * list.ChunkSize), length: sizes[0]}}, nil
	case rq2fixture.KindDirectReplace:
		data, err := payload(0)
		if err != nil {
			return nil, err
		}
		item, err := write(operation.SourcePath, data)
		return []changedFileRange{item}, err
	case rq2fixture.KindDirectInsert:
		if _, err := os.Stat(filepath.Join(w.root, filepath.FromSlash(operation.DestinationPath))); !os.IsNotExist(err) {
			return nil, fmt.Errorf("destination %q is not absent", operation.DestinationPath)
		}
		data, err := payload(0)
		if err != nil {
			return nil, err
		}
		item, err := write(operation.DestinationPath, data)
		return []changedFileRange{item}, err
	case rq2fixture.KindBatchInsert:
		changed := make([]changedFileRange, len(sizes))
		for index := range sizes {
			if _, err := os.Stat(filepath.Join(w.root, filepath.FromSlash(operation.Batch[index].Path))); !os.IsNotExist(err) {
				return nil, fmt.Errorf("batch destination %q is not absent", operation.Batch[index].Path)
			}
			data, err := payload(index)
			if err != nil {
				return nil, err
			}
			item, err := write(operation.Batch[index].Path, data)
			if err != nil {
				return nil, err
			}
			changed[index] = item
		}
		return changed, nil
	case rq2fixture.KindDirectDelete:
		return nil, os.Remove(filepath.Join(w.root, filepath.FromSlash(operation.SourcePath)))
	case rq2fixture.KindDirectMove:
		destination := filepath.Join(w.root, filepath.FromSlash(operation.DestinationPath))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return nil, err
		}
		return nil, os.Rename(filepath.Join(w.root, filepath.FromSlash(operation.SourcePath)), destination)
	default:
		return nil, fmt.Errorf("unsupported native filesystem operation kind %q", operation.Kind)
	}
}

func payloadShape(operation rq2fixture.Operation) (int, []int, error) {
	switch operation.Kind {
	case rq2fixture.KindDirectDelete, rq2fixture.KindDirectMove:
		return 0, nil, nil
	case rq2fixture.KindBatchInsert:
		sizes := make([]int, len(operation.Batch))
		for index, target := range operation.Batch {
			sizes[index] = int(target.PayloadBytes)
		}
		return len(sizes), sizes, nil
	default:
		return 1, []int{int(operation.PayloadBytes)}, nil
	}
}

func mutationDigest(value []byte) [32]byte { return sha256.Sum256(value) }
