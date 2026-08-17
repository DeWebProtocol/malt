package capability

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestValidateBindingRejectsWrongDatasetOrBranchBeforeUse(t *testing.T) {
	for _, test := range []struct {
		name    string
		binding DatasetBinding
		wantErr bool
	}{
		{name: "matching main", binding: DatasetBinding{DatasetID: "dataset-one", Branch: "main"}},
		{name: "canonical explicit branch", binding: DatasetBinding{DatasetID: "dataset-one", Branch: "heads/team/photos"}},
		{name: "wrong dataset", binding: DatasetBinding{DatasetID: "dataset-two", Branch: "main"}, wantErr: true},
		{name: "wrong branch", binding: DatasetBinding{DatasetID: "dataset-one", Branch: "other"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			branch := "main"
			if strings.Contains(test.name, "explicit") {
				branch = "team/photos"
			}
			err := ValidateBinding("dataset-one", branch, test.binding)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateBinding error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestValidateApplyResultBindsExactRequest(t *testing.T) {
	base := capabilityCID(t, "base")
	candidateRoot := capabilityCID(t, "candidate")
	now := time.Date(2026, time.August, 17, 1, 2, 3, 0, time.UTC)
	request := ApplyRequest{
		OperationID: "operation-one", BaseCommit: "commit-base", BaseRoot: base.String(),
		CandidateRoot: candidateRoot.String(), BaseRevision: 7, Message: "backup",
	}
	candidate := Commit{
		ID: "commit-candidate", DatasetID: "dataset-one", Root: candidateRoot.String(),
		Parents: []string{"commit-base"}, BaseRoot: base.String(), Author: "device-one",
		Message: "backup", CreatedAt: now,
	}
	valid := ApplyResult{
		Status: "fast_forward",
		Head: ObservedHead{
			DatasetID: "dataset-one", Name: "main", Kind: "main", State: "open",
			CommitID: candidate.ID, Root: candidate.Root, Revision: 8, CreatedAt: now, UpdatedAt: now,
		},
		Candidate: candidate,
		Commit:    candidate,
	}
	if err := ValidateApplyResult("dataset-one", request, valid); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ApplyResult)
	}{
		{name: "wrong head dataset", mutate: func(value *ApplyResult) { value.Head.DatasetID = "dataset-two" }},
		{name: "wrong candidate root", mutate: func(value *ApplyResult) { value.Candidate.Root = base.String() }},
		{name: "head points elsewhere", mutate: func(value *ApplyResult) { value.Head.CommitID = "another" }},
		{name: "duplicate commit parent", mutate: func(value *ApplyResult) {
			value.Candidate.Parents = []string{"commit-base", "commit-base"}
			value.Commit.Parents = append([]string(nil), value.Candidate.Parents...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Candidate.Parents = append([]string(nil), valid.Candidate.Parents...)
			value.Commit.Parents = append([]string(nil), valid.Commit.Parents...)
			test.mutate(&value)
			if err := ValidateApplyResult("dataset-one", request, value); err == nil {
				t.Fatal("tampered apply result was accepted")
			}
		})
	}
}

func TestDatasetCapabilityJSONPreservesExistingPersistentFieldNames(t *testing.T) {
	value := ApplyResult{
		Status:    "fast_forward",
		Head:      ObservedHead{DatasetID: "dataset-one"},
		Candidate: Commit{DatasetID: "dataset-one"},
		Commit:    Commit{DatasetID: "dataset-one"},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(data)
	if strings.Count(encoded, `"bucket_id":"dataset-one"`) != 3 {
		t.Fatalf("dataset JSON changed persisted Bucket identity fields: %s", encoded)
	}
	if strings.Contains(encoded, "dataset_id") {
		t.Fatalf("dataset JSON introduced an incompatible field: %s", encoded)
	}
	request, err := json.Marshal(ApplyRequest{OperationID: "retry-one"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(request), `"push_id":"retry-one"`) || strings.Contains(string(request), "operation_id") {
		t.Fatalf("apply request JSON changed retry identity: %s", request)
	}
}

func capabilityCID(t *testing.T, body string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(body), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}
