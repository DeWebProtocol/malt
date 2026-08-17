package bucketsync

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	gatewaytransport "github.com/dewebprotocol/malt-client/transport"
)

type compatibilityGateway struct{}

func (compatibilityGateway) BucketHead(context.Context) (*gatewaytransport.BucketRef, error) {
	return nil, nil
}

func (compatibilityGateway) PushBucket(context.Context, gatewaytransport.BucketPushRequest) (*gatewaytransport.BucketPushResult, error) {
	return nil, nil
}

func TestGatewayCompatibilityOpenTrimsBucketID(t *testing.T) {
	service, err := Open(filepath.Join(t.TempDir(), "workspace.json"), compatibilityGateway{}, " dataset-one ")
	if err != nil {
		t.Fatal(err)
	}
	if service.bucketID != "dataset-one" || service.remote.DatasetBinding().DatasetID != "dataset-one" {
		t.Fatalf("legacy Bucket binding was not normalized: service=%q binding=%#v", service.bucketID, service.remote.DatasetBinding())
	}
}

func TestGatewayCompatibilityConversionPreservesPersistedResultJSON(t *testing.T) {
	now := time.Date(2026, time.August, 17, 1, 2, 3, 0, time.UTC)
	root := testCID(t, "compat-root").String()
	commit := gatewaytransport.BucketCommit{
		ID: "commit-one", BucketID: "dataset-one", Root: root,
		Parents: []string{"commit-base"}, BaseRoot: testCID(t, "compat-base").String(),
		Author: "device-one", Credential: "credential-one", ChangeSetCID: testCID(t, "change").String(),
		Message: "backup", CreatedAt: now,
	}
	head := gatewaytransport.BucketRef{
		BucketID: "dataset-one", Name: "main", Kind: "main", State: "open",
		CommitID: commit.ID, Root: root, Revision: 2, CreatedBy: "device-one", CreatedAt: now, UpdatedAt: now,
	}
	branch := head
	branch.Name, branch.Kind = "conflicts/device/one", "conflict"
	legacy := gatewaytransport.BucketPushResult{
		Status: "branched", Head: head, Candidate: commit, Commit: commit, Branch: &branch,
		MergeBase: commit.BaseRoot,
		Conflicts: []gatewaytransport.BucketConflict{{Coordinate: "docs/readme", Base: "a", Local: "b", Remote: "c"}},
	}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	semanticJSON, err := json.Marshal(applyResultFromGateway(legacy))
	if err != nil {
		t.Fatal(err)
	}
	if string(semanticJSON) != string(legacyJSON) {
		t.Fatalf("semantic result changed persisted JSON\nlegacy:   %s\nsemantic: %s", legacyJSON, semanticJSON)
	}
}
