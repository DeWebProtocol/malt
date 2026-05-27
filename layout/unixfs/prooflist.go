package unixfs

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/auth/semantic"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// ListIndexStep records one list index proof used by a UnixFS large-file
// payload read. Multiple ListIndexStep values compose range evidence; they do
// not claim a first-class range proof.
type ListIndexStep struct {
	Root   cid.Cid
	Index  uint64
	Length uint64
	Target cid.Cid
	Proof  structure.Proof
}

// ProofListFromSteps converts UnixFS layout resolution steps into the
// verifier-facing ProofList schema. It is an adapter over existing layout
// proofs and does not change AddFile, Resolve, or ReadFile behavior.
func ProofListFromSteps(root cid.Cid, queriedPath string, steps []Step) (*prooflist.ProofList, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root is undefined")
	}

	pl := &prooflist.ProofList{
		Root:  root,
		Query: queriedPath,
		Steps: make([]prooflist.Step, 0, len(steps)),
	}
	for _, layoutStep := range steps {
		path := layoutStep.Path.String()
		pl.Steps = append(pl.Steps, prooflist.Step{
			Kind:            unixFSStepKind(path),
			From:            layoutStep.Root,
			Query:           queriedPath,
			Path:            path,
			Target:          layoutStep.Target,
			EvidenceKind:    "structure",
			EvidenceBackend: "map",
			Proof:           cloneProofBytes(layoutStep.Proof),
		})
	}
	return pl, nil
}

// AppendListIndexSteps appends typed list-index evidence to an existing
// ProofList. This is intended for large-file range reads that are currently
// represented as composed index proofs.
func AppendListIndexSteps(pl *prooflist.ProofList, queriedPath string, steps []ListIndexStep) error {
	if pl == nil {
		return fmt.Errorf("prooflist is nil")
	}
	for _, layoutStep := range steps {
		if !layoutStep.Root.Defined() {
			return fmt.Errorf("list step root is undefined")
		}
		if !layoutStep.Target.Defined() {
			return fmt.Errorf("list step target is undefined")
		}
		index := layoutStep.Index
		length := layoutStep.Length
		pl.Steps = append(pl.Steps, prooflist.Step{
			Kind:            prooflist.KindListIndex,
			From:            layoutStep.Root,
			Query:           queriedPath,
			Coordinate:      strconv.FormatUint(layoutStep.Index, 10),
			Index:           &index,
			Length:          &length,
			Target:          layoutStep.Target,
			EvidenceKind:    "structure",
			EvidenceBackend: "list",
			Proof:           cloneProofBytes(layoutStep.Proof),
		})
	}
	return nil
}

// AppendListRangeStep appends one measured-list range evidence step. The proof
// payload is produced by the list semantic and internally composes metadata and
// index proofs for the minimum segment set.
func AppendListRangeStep(pl *prooflist.ProofList, queriedPath string, root cid.Cid, start, end uint64, result list.RangeResult, proof structure.Proof) error {
	if pl == nil {
		return fmt.Errorf("prooflist is nil")
	}
	if !root.Defined() {
		return fmt.Errorf("list range root is undefined")
	}
	childCount := result.Metadata.ChildCount
	totalSize := result.Metadata.TotalSize
	chunkSize := result.Metadata.ChunkSize
	segments := append([]cid.Cid(nil), result.Segments...)
	for i, segment := range segments {
		if !segment.Defined() {
			return fmt.Errorf("list range segment %d is undefined", i)
		}
	}
	pl.Steps = append(pl.Steps, prooflist.Step{
		Kind:            prooflist.KindListRange,
		From:            root,
		Query:           queriedPath,
		Coordinate:      fmt.Sprintf("%d:%d", start, end),
		Start:           &start,
		End:             &end,
		ChildCount:      &childCount,
		TotalSize:       &totalSize,
		ChunkSize:       &chunkSize,
		Target:          root,
		Segments:        segments,
		EvidenceKind:    "structure",
		EvidenceBackend: "measured_list",
		Proof:           cloneProofBytes(proof),
	})
	return nil
}

// VerifyProofListListStructure verifies list-backed ProofList structure steps
// emitted by the UnixFS layout. Server-side ProofList verification owns the
// ordered traversal policy; UnixFS owns the list/index/range proof semantics.
func VerifyProofListListStructure(lists list.Semantics, step prooflist.Step, stepIndex int) (bool, error) {
	switch step.EvidenceBackend {
	case "list":
		if step.Index == nil {
			return false, fmt.Errorf("prooflist step %d list index is missing", stepIndex)
		}
		if step.Length == nil {
			return false, fmt.Errorf("prooflist step %d list length is missing", stepIndex)
		}
		return lists.Verify(step.From, *step.Index, list.Query{Key: step.Target, Length: *step.Length}, structure.Proof(step.Proof))
	case "measured_list":
		if !step.Target.Equals(step.From) {
			return false, nil
		}
		if step.Start == nil {
			return false, fmt.Errorf("prooflist step %d list range start is missing", stepIndex)
		}
		if step.ChildCount == nil {
			return false, fmt.Errorf("prooflist step %d list range child count is missing", stepIndex)
		}
		if step.TotalSize == nil {
			return false, fmt.Errorf("prooflist step %d list range total size is missing", stepIndex)
		}
		if step.ChunkSize == nil {
			return false, fmt.Errorf("prooflist step %d list range chunk size is missing", stepIndex)
		}
		measured, ok := lists.(list.MeasuredSemantics)
		if !ok {
			return false, fmt.Errorf("prooflist step %d has measured list evidence but graph list semantic does not support measured ranges", stepIndex)
		}
		return measured.VerifyRange(step.From, *step.Start, step.End, list.RangeResult{
			Metadata: list.RangeMetadata{
				ChildCount: *step.ChildCount,
				TotalSize:  *step.TotalSize,
				ChunkSize:  *step.ChunkSize,
			},
			Segments: append([]cid.Cid(nil), step.Segments...),
		}, structure.Proof(step.Proof))
	default:
		return false, fmt.Errorf("prooflist step %d has unsupported structure evidence backend %q", stepIndex, step.EvidenceBackend)
	}
}

// AppendListPayloadRangeProof appends proof evidence for a byte range in a
// fixed-width list payload. It prefers the measured-list range proof and falls
// back to composed list-index proof steps when the measured proof is not
// available.
func (l *Layout) AppendListPayloadRangeProof(ctx context.Context, pl *prooflist.ProofList, queriedPath string, root cid.Cid, offset, length uint64) error {
	if length == 0 {
		return nil
	}
	start := offset
	end := offset + length
	if measured, ok := l.lists.(list.MeasuredSemantics); ok {
		result, proof, err := measured.ProveRange(ctx, l.namespace, root, start, &end)
		if err == nil {
			return AppendListRangeStep(pl, queriedPath, root, start, end, result, proof)
		}
	}
	steps, err := l.listIndexStepsForPayloadRange(ctx, root, offset, length, uint64(l.chunkSize))
	if err != nil {
		return err
	}
	return AppendListIndexSteps(pl, queriedPath, steps)
}

// ListIndexStepsForFileRange returns composed list-index proof steps for a
// large-file range. Small raw-payload files return no list steps.
func (l *Layout) ListIndexStepsForFileRange(ctx context.Context, root cid.Cid, path string, offset, length uint64) ([]ListIndexStep, error) {
	if length == 0 {
		return nil, nil
	}
	info, err := l.resolveFile(ctx, root, path)
	if err != nil {
		return nil, err
	}
	if maltcid.SemanticKindOf(info.payload) != maltcid.SemanticKindList {
		return nil, nil
	}
	if offset >= info.size {
		return nil, nil
	}
	if length > info.size-offset {
		length = info.size - offset
	}

	return l.listIndexStepsForPayloadRange(ctx, info.payload, offset, length, info.chunkSize)
}

func (l *Layout) listIndexStepsForPayloadRange(ctx context.Context, root cid.Cid, offset, length, chunkSize uint64) ([]ListIndexStep, error) {
	if length == 0 {
		return nil, nil
	}
	if chunkSize == 0 {
		chunkSize = uint64(l.chunkSize)
	}
	startIndex := offset / chunkSize
	endOffset := offset + length
	endIndex := (endOffset - 1) / chunkSize
	steps := make([]ListIndexStep, 0, endIndex-startIndex+1)
	for index := startIndex; index <= endIndex; index++ {
		query, proof, err := l.lists.Prove(ctx, l.namespace, root, index)
		if err != nil {
			return nil, err
		}
		ok, err := l.lists.Verify(root, index, query, proof)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("list proof failed at index %d", index)
		}
		if !query.Key.Defined() {
			return nil, fmt.Errorf("%w: missing chunk %d", ErrNotFound, index)
		}
		steps = append(steps, ListIndexStep{
			Root:   root,
			Index:  index,
			Length: query.Length,
			Target: query.Key,
			Proof:  proof,
		})
	}
	return steps, nil
}

func unixFSStepKind(path string) prooflist.StepKind {
	if path == payloadPath.String() {
		return prooflist.KindPayloadBinding
	}
	return prooflist.KindMapStep
}

func cloneProofBytes(proof []byte) []byte {
	out := make([]byte, len(proof))
	copy(out, proof)
	return out
}
