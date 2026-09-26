package writeplan

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

type blockBatchWriter struct {
	blockFunc
	batch func(context.Context, []cas.Block) ([]cas.PutResult, error)
}

func (w blockBatchWriter) PutBatch(ctx context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	return w.batch(ctx, blocks)
}

func batchResults(t *testing.T, blocks []cas.Block) []cas.PutResult {
	t.Helper()
	results := make([]cas.PutResult, len(blocks))
	for i, block := range blocks {
		key, err := cas.CIDForBlock(block)
		if err != nil {
			t.Fatal(err)
		}
		results[i] = cas.PutResult{CID: key, Status: cas.PutStatusStored}
	}
	return results
}

func TestPreparedBlockBatchesBoundMemoryAndPreserveCodecs(t *testing.T) {
	for _, shape := range []struct {
		name                  string
		count, size           int
		wantBatches, wantPuts int
	}{
		{"count", maxBatchBlocks + 1, 1, 2, 0},
		{"bytes", 3, maxBatchBytes/2 + 1, 3, 0},
		{"large-single", 1, maxBatchBytes + 1, 0, 1},
	} {
		t.Run(shape.name, func(t *testing.T) {
			p := Plan{}
			for i := 0; i < shape.count; i++ {
				body := make([]byte, shape.size)
				body[0] = byte(i)
				codec := uint64(cid.Raw)
				if i%2 == 0 {
					codec = cid.DagJSON
				}
				key, err := cas.CIDForBlock(cas.Block{Data: body, Codec: codec})
				if err != nil {
					t.Fatal(err)
				}
				p.Blocks = append(p.Blocks, Bytes(key, body))
			}
			batches, puts, received := 0, 0, 0
			writer := blockBatchWriter{
				blockFunc: func(_ context.Context, body []byte, codec uint64) (cid.Cid, error) {
					puts++
					received++
					return cas.CIDForBlock(cas.Block{Data: body, Codec: codec})
				},
				batch: func(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
					batches++
					received += len(blocks)
					size := 0
					for _, block := range blocks {
						size += len(block.Data)
					}
					if len(blocks) > maxBatchBlocks || size > maxBatchBytes {
						t.Fatalf("batch exceeded bounds: %d blocks, %d bytes", len(blocks), size)
					}
					return batchResults(t, blocks), nil
				},
			}
			if err := p.persistBlocks(t.Context(), writer); err != nil {
				t.Fatal(err)
			}
			if batches != shape.wantBatches || puts != shape.wantPuts || received != shape.count {
				t.Fatalf("got %d batches, %d puts, %d blocks", batches, puts, received)
			}
		})
	}
}

func TestPreparedBlockBatchFailurePreventsCandidateInstallation(t *testing.T) {
	for _, failure := range []string{"length", "CID", "status", "lost-acknowledgement"} {
		t.Run(failure, func(t *testing.T) {
			p := fixture(t)
			key, err := cas.CIDForBlock(cas.Block{Data: []byte("payload")})
			if err != nil {
				t.Fatal(err)
			}
			p.Blocks = []Block{Bytes(key, []byte("payload"))}
			writer := blockBatchWriter{batch: func(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
				results := batchResults(t, blocks)
				switch failure {
				case "length":
					return nil, nil
				case "CID":
					blocks[0].Data[0] = '!'
					return batchResults(t, blocks), nil
				case "status":
					results[0].Status = "queued"
				case "lost-acknowledgement":
					return nil, errors.New("lost acknowledgement")
				}
				return results, nil
			}}
			candidates := 0
			remote := candidateFunc(func(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error) {
				candidates++
				return p.Root, nil
			})
			if err := p.Persist(t.Context(), writer, remote); err == nil || candidates != 0 {
				t.Fatalf("invalid batch reached candidate installation: %v, %d", err, candidates)
			}
			writer.batch = func(_ context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
				return batchResults(t, blocks), nil
			}
			if err := p.Persist(t.Context(), writer, remote); err != nil || candidates != 1 {
				t.Fatalf("exact retry failed: %v, %d", err, candidates)
			}
		})
	}
}
