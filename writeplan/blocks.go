package writeplan

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/internal/cas"
	cid "github.com/ipfs/go-cid"
)

// BlockWriter preserves the writer's optional cas.BatchWriter capability.
// The executor bounds batches and checks each acknowledgement before installing
// any candidate. Adapters without batch support retain single-block behavior.
type BlockWriter interface {
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
}

// These are executor memory bounds, not transport or protocol limits. A larger
// individual block takes the single-block path instead of enlarging a batch.
const (
	maxBatchBlocks = 4096
	maxBatchBytes  = 8 << 20
)

func readBlock(ctx context.Context, block Block) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := block.Read(ctx)
	if err != nil {
		return nil, err
	}
	key, err := block.CID.Prefix().Sum(body)
	if err != nil || !key.Equals(block.CID) {
		return nil, fmt.Errorf("local write-plan bytes differ from CID %s", block.CID)
	}
	return body, nil
}

func (p Plan) persistBlocks(ctx context.Context, writer BlockWriter) error {
	if len(p.Blocks) > 0 && writer == nil {
		return fmt.Errorf("write plan requires a block writer")
	}
	// Check every local dependency before the first remote side effect, without
	// holding the entire payload in memory. Check again when consuming each body.
	for _, block := range p.Blocks {
		if _, err := readBlock(ctx, block); err != nil {
			return err
		}
	}
	batchWriter, batching := writer.(cas.BatchWriter)
	var pending []cas.Block
	var expected []cid.Cid
	bytes := 0
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		results, err := batchWriter.PutBatch(ctx, pending)
		if err != nil {
			return err
		}
		if len(results) != len(expected) {
			return fmt.Errorf("remote CAS returned %d results for %d planned blocks", len(results), len(expected))
		}
		// Expected identities are independent of an adapter's mutable input.
		for i, result := range results {
			if !result.CID.Equals(expected[i]) {
				return fmt.Errorf("remote CAS substituted planned CID %s with %s", expected[i], result.CID)
			}
			switch result.Status {
			case cas.PutStatusStored, cas.PutStatusAlreadyPresent, cas.PutStatusDuplicate,
				cas.PutStatusNewlyPersisted, cas.PutStatusDuplicateInRequest:
			default:
				return fmt.Errorf("remote CAS returned unsupported write status %q", result.Status)
			}
		}
		pending, expected, bytes = nil, nil, 0
		return nil
	}
	for _, block := range p.Blocks {
		body, err := readBlock(ctx, block)
		if err != nil {
			return err
		}
		if !batching || len(body) > maxBatchBytes {
			if err := flush(); err != nil {
				return err
			}
			got, err := writer.PutWithCodec(ctx, body, block.CID.Prefix().Codec)
			if err != nil {
				return err
			}
			if !got.Equals(block.CID) {
				return fmt.Errorf("remote CAS substituted planned CID %s with %s", block.CID, got)
			}
			continue
		}
		if len(pending) == maxBatchBlocks || len(body) > maxBatchBytes-bytes {
			if err := flush(); err != nil {
				return err
			}
		}
		pending = append(pending, cas.Block{Data: body, Codec: block.CID.Prefix().Codec})
		expected = append(expected, block.CID)
		bytes += len(body)
	}
	return flush()
}
