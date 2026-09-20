package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"
	"github.com/dewebprotocol/malt-client/transport"
	cid "github.com/ipfs/go-cid"
)

const (
	categoryLogicalPayload = "logical-changed-payload"
	categoryCASMetadata    = "cas-structural-metadata"
	modeSidecarSchema      = "malt-eval-rq3-file-mode/v1"
)

type logicalFile struct {
	data    []byte
	mode    uint32
	digest  string
	size    int64
	payload cid.Cid
}

type classifiedBlock struct {
	block    transport.Block
	category string
	cause    string
	suffix   string
}

type fileChange struct {
	path   string
	before *logicalFile
	after  *logicalFile
}

func initialLogicalState(snapshot rq3baseline.Snapshot, chunkBytes uint64, payloads *logicalPayloadStore) (map[string]logicalFile, []classifiedBlock, error) {
	state := make(map[string]logicalFile, len(snapshot.Files))
	blocks := make([]classifiedBlock, 0)
	for _, file := range snapshot.Files {
		data, err := decodeFrozenPayload(file.PayloadBase64)
		if err != nil || file.Mode == nil {
			return nil, nil, fmt.Errorf("decode prevalidated snapshot file %q: %w", file.Path, err)
		}
		value, err := payloads.retain(data, *file.Mode, file.PayloadSHA256)
		if err != nil {
			return nil, nil, fmt.Errorf("retain snapshot file %q: %w", file.Path, err)
		}
		state[file.Path] = value
		materialized := value
		materialized.data = data
		fileBlocks, err := fileCASBlocks(materialized, true, chunkBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("normalize snapshot file %q: %w", file.Path, err)
		}
		blocks = append(blocks, fileBlocks...)
	}
	return state, blocks, nil
}

// applyFrozenCommit executes the already baseline-validated source semantics
// in their frozen order. CAS attempts retain intermediate full-result payloads
// even though one client root closes the complete commit.
func applyFrozenCommit(state map[string]logicalFile, commit rq3baseline.Commit, chunkBytes uint64, changedChunksOnly bool, payloads *logicalPayloadStore) ([]classifiedBlock, int64, []fileChange, error) {
	blocks := make([]classifiedBlock, 0)
	var logicalPayloadBytes int64
	before := make(map[string]*logicalFile)
	capture := func(path string) {
		if _, captured := before[path]; captured {
			return
		}
		if file, exists := state[path]; exists {
			copy := file
			before[path] = &copy
		} else {
			before[path] = nil
		}
	}
	for index, change := range commit.Mutations {
		capture(change.Path)
		if change.Destination != "" {
			capture(change.Destination)
		}
		old := state[change.Path]
		switch change.Kind {
		case rq3baseline.MutationInsert, rq3baseline.MutationReplace, rq3baseline.MutationAppend:
			data, err := decodeFrozenPayload(change.PayloadBase64)
			if err != nil || change.Mode == nil {
				return nil, 0, nil, fmt.Errorf("mutation[%d] decode payload: %w", index, err)
			}
			next, err := payloads.retain(data, *change.Mode, change.PayloadSHA256)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("mutation[%d] retain payload: %w", index, err)
			}
			includeMode := change.Kind == rq3baseline.MutationInsert || old.mode != next.mode
			materialized := next
			materialized.data = data
			fileBlocks, err := fileCASBlocks(materialized, includeMode, chunkBytes)
			if err != nil {
				return nil, 0, nil, fmt.Errorf("mutation[%d] normalize file: %w", index, err)
			}
			blocks = append(blocks, fileBlocks...)
			var changed int64
			switch change.Kind {
			case rq3baseline.MutationInsert:
				changed = int64(len(data))
			case rq3baseline.MutationAppend:
				changed = int64(len(data)) - old.size
			case rq3baseline.MutationReplace:
				if changedChunksOnly {
					oldData, readErr := payloads.read(old)
					if readErr != nil {
						return nil, 0, nil, fmt.Errorf("mutation[%d] read old payload: %w", index, readErr)
					}
					changed, err = changedFixedChunkBytes(oldData, data, chunkBytes)
				} else {
					changed = int64(len(data))
				}
			}
			if err != nil {
				return nil, 0, nil, fmt.Errorf("mutation[%d] logical payload accounting: %w", index, err)
			}
			if changed < 0 || logicalPayloadBytes > int64(^uint64(0)>>1)-changed {
				return nil, 0, nil, fmt.Errorf("mutation[%d] logical payload accounting overflow", index)
			}
			logicalPayloadBytes += changed
			state[change.Path] = next

		case rq3baseline.MutationModeChange:
			if change.Mode == nil {
				return nil, 0, nil, fmt.Errorf("mutation[%d] mode is absent after semantic prevalidation", index)
			}
			old.mode = *change.Mode
			state[change.Path] = old
			modeBlock, err := modeCASBlock(old.mode)
			if err != nil {
				return nil, 0, nil, err
			}
			blocks = append(blocks, modeBlock)

		case rq3baseline.MutationDelete:
			delete(state, change.Path)

		case rq3baseline.MutationRename, rq3baseline.MutationMove:
			delete(state, change.Path)
			state[change.Destination] = old

		default:
			return nil, 0, nil, fmt.Errorf("mutation[%d] kind %q escaped semantic prevalidation", index, change.Kind)
		}
	}
	paths := make([]string, 0, len(before))
	for path := range before {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	changes := make([]fileChange, 0, len(paths))
	for _, path := range paths {
		var after *logicalFile
		if file, exists := state[path]; exists {
			copy := file
			after = &copy
		}
		if logicalFileEqual(before[path], after) {
			continue
		}
		changes = append(changes, fileChange{path: path, before: before[path], after: after})
	}
	return blocks, logicalPayloadBytes, changes, nil
}

func logicalFileEqual(a, b *logicalFile) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.mode == b.mode && a.digest == b.digest && a.size == b.size && a.payload.Equals(b.payload)
}

func changedFixedChunkBytes(before, after []byte, chunkBytes uint64) (int64, error) {
	if chunkBytes == 0 || chunkBytes > uint64(^uint(0)>>1) {
		return 0, fmt.Errorf("chunk size is outside host bounds")
	}
	size := int(chunkBytes)
	var total int64
	for offset := 0; offset < len(after); offset += size {
		end := min(len(after), offset+size)
		unchanged := end <= len(before) && slices.Equal(before[offset:end], after[offset:end])
		if unchanged {
			continue
		}
		if total > int64(^uint64(0)>>1)-int64(end-offset) {
			return 0, fmt.Errorf("changed chunk bytes overflow")
		}
		total += int64(end - offset)
	}
	return total, nil
}

func decodeFrozenPayload(encoded string) ([]byte, error) {
	value, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(value) != encoded {
		return nil, fmt.Errorf("payload is not canonical standard base64")
	}
	return value, nil
}

func fileCASBlocks(file logicalFile, includeMode bool, chunkBytes uint64) ([]classifiedBlock, error) {
	if chunkBytes == 0 {
		return nil, fmt.Errorf("fixed chunk size is zero")
	}
	result := []classifiedBlock{{
		block:    transport.Block{Codec: cid.Raw, Data: append([]byte(nil), file.data...)},
		category: categoryLogicalPayload, cause: "flat-whole-file-blob", suffix: "payload",
	}}
	if includeMode {
		mode, err := modeCASBlock(file.mode)
		if err != nil {
			return nil, err
		}
		result = append(result, mode)
	}
	return result, nil
}

func modeCASBlock(mode uint32) (classifiedBlock, error) {
	wire := struct {
		SchemaVersion string `json:"schema_version"`
		FileKind      string `json:"file_kind"`
		Mode          uint32 `json:"mode"`
	}{SchemaVersion: modeSidecarSchema, FileKind: rq3baseline.FileKindRegular, Mode: mode}
	raw, err := json.Marshal(wire)
	if err != nil {
		return classifiedBlock{}, err
	}
	return classifiedBlock{
		block: transport.Block{Codec: cid.DagJSON, Data: raw}, category: categoryCASMetadata,
		cause: "file-mode-sidecar", suffix: "mode-sidecar",
	}, nil
}
