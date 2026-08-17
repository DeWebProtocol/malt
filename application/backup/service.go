package backup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/bucketsync"
	cid "github.com/ipfs/go-cid"
)

const restoreRangeSize = uint64(4 << 20)

var (
	ErrPendingWorkspace = errors.New("Bucket has pending or branched local work")
	ErrBackupConflict   = errors.New("backup candidate could not be merged")
	ErrProtectedSource  = errors.New("backup source contains MALT runtime key or state")
	ErrUnacceptedRoot   = errors.New("remote backup root is not locally accepted")
)

type KeySource interface {
	ActiveEpoch() uint32
	BucketKey(epoch uint32, bucketID string) ([32]byte, error)
}

type Sync interface {
	Pull(context.Context) (bucketsync.Workspace, error)
	Status() (bucketsync.Workspace, error)
	CurrentBase(cid.Cid) (bucketsync.Head, error)
	Stage(cid.Cid, bucketsync.Head, cid.Cid, string) (bucketsync.Stash, error)
	RestorePending(bucketsync.Stash) (bucketsync.Stash, error)
	Push(context.Context, cid.Cid, cid.Cid, string) (bucketsync.PushOutcome, error)
	ResolveBranched(stashID, candidateRoot string) (bucketsync.Workspace, error)
}

type Result struct {
	PlanID              string                 `json:"plan_id,omitempty"`
	PlanName            string                 `json:"plan_name,omitempty"`
	Branch              string                 `json:"branch,omitempty"`
	Source              string                 `json:"source"`
	RemotePath          string                 `json:"remote_path"`
	KeyEpoch            uint32                 `json:"key_epoch"`
	EncryptedBytes      int64                  `json:"encrypted_bytes"`
	SourceFingerprint   string                 `json:"source_fingerprint"`
	BindingFingerprints map[string]string      `json:"binding_fingerprints,omitempty"`
	ManifestFingerprint string                 `json:"manifest_fingerprint,omitempty"`
	ChangedBindings     []string               `json:"changed_bindings,omitempty"`
	Skipped             bool                   `json:"skipped,omitempty"`
	Base                bucketsync.Head        `json:"base"`
	CandidateRoot       string                 `json:"candidate_root"`
	Push                bucketsync.PushOutcome `json:"push"`
	CompletedAt         time.Time              `json:"completed_at"`
	RetriedPending      bool                   `json:"retried_pending,omitempty"`
	ReconciledPending   bool                   `json:"reconciled_pending,omitempty"`
}

func ValidateSource(source string, protected []string) error {
	for _, candidate := range protected {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		overlap, err := bindingSourcesOverlap(source, candidate)
		if err != nil {
			return fmt.Errorf("compare backup source and protected runtime path: %w", err)
		}
		if overlap {
			return fmt.Errorf("%w %s; choose a narrower source or move runtime state to an owner-only directory outside it", ErrProtectedSource, candidate)
		}
	}
	return nil
}

func pendingStash(workspace bucketsync.Workspace, pending PendingBackup, candidate string) (bucketsync.Stash, bool, error) {
	for _, stash := range workspace.Stashes {
		if pending.StashID != "" {
			if stash.ID != pending.StashID {
				continue
			}
			if stash.PushID != pending.PushID || stash.CandidateRoot != candidate ||
				stash.Base != pending.Result.Base || stash.Message != pending.Message ||
				stash.ChangeSetCID != "" || (stash.Status != "pending" && stash.Status != "branched") {
				return bucketsync.Stash{}, false, fmt.Errorf("pending backup stash %s conflicts with its journaled push identity", pending.StashID)
			}
			return stash, true, nil
		}
		if stash.CandidateRoot == candidate && (stash.Status == "pending" || stash.Status == "branched") {
			if stash.Base != pending.Result.Base || stash.Message != pending.Message || stash.ChangeSetCID != "" {
				return bucketsync.Stash{}, false, fmt.Errorf("pending backup candidate %s conflicts with an existing Bucket stash", candidate)
			}
			return stash, true, nil
		}
	}
	return bucketsync.Stash{}, false, nil
}
