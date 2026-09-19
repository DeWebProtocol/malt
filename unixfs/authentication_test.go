package unixfs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/dewebprotocol/malt-client/cache"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/journal"
	unixfsclientroot "github.com/dewebprotocol/malt-client/unixfs/clientroot"
	clientverifier "github.com/dewebprotocol/malt-core/sdk/verifier"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/unixfs"
	materialmemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

type typedRemote struct {
	*realRemote
	nodes        *materialmemory.Nodes
	candidates   map[string]protocol.AuthenticationCandidate
	wrongReceipt bool
}

func newTypedRemote(t *testing.T) *typedRemote {
	return &typedRemote{realRemote: newRealRemote(t), nodes: materialmemory.NewNodes(), candidates: map[string]protocol.AuthenticationCandidate{}}
}
func (r *typedRemote) Authenticate(ctx context.Context, q protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	result, err := authentication.Execute(ctx, r.graph.Authentication(), q, r.nodes)
	return &result, err
}
func (r *typedRemote) AuthenticationCandidate(ctx context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	candidate, ok := r.candidates[root.KeyString()]
	if !ok {
		return nil, errors.New("candidate absent")
	}
	exported, err := authentication.Export(ctx, r.graph.Authentication(), root, candidate.State, r.nodes)
	return &exported, err
}
func (r *typedRemote) MaterializeAuthentication(ctx context.Context, c protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := authentication.Materialize(ctx, r.graph.Authentication(), c, r.nodes); err != nil {
		return cid.Undef, err
	}
	root := cid.MustParse(c.Root)
	r.candidates[root.KeyString()] = c
	if r.wrongReceipt {
		return cid.MustParse("bafkqaaa"), nil
	}
	return root, nil
}
func TestRootedUnixFSWritesReadsAndPreservesHistory(t *testing.T) {
	remote := newTypedRemote(t)
	layout, err := unixfs.NewLayout(unixfs.LayoutRootedV1)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: remote, Blocks: remote, Layout: layout, ChunkSize: 8})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := writer.EmptyDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := writer.AddDirectory(t.Context(), empty.CandidateRoot, "nested")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("a rooted file with several chunks")
	file, err := writer.AddFile(t.Context(), directory.CandidateRoot, "nested/file", body)
	if err != nil {
		t.Fatal(err)
	}
	if file.Accepted {
		t.Fatal("write promoted trusted Root")
	}
	result, err := writer.ReadFileRange(t.Context(), file.CandidateRoot, "nested/file", 5, 17)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Body, body[5:22]) || result.Authentication == nil {
		t.Fatal("wrong typed range")
	}
	for _, entry := range remote.candidates[file.CandidateRoot.KeyString()].State.Entries {
		if strings.Contains(string(entry.Input.Data), "/") {
			t.Fatal("rooted directory included flattened aliases")
		}
	}
	removed, err := writer.RemovePath(t.Context(), file.CandidateRoot, "nested/file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Stat(t.Context(), removed.CandidateRoot, "nested/file"); !errors.Is(err, unixfs.ErrNotFound) {
		t.Fatalf("deleted file: %v", err)
	}
	historical, err := writer.ReadFile(t.Context(), file.CandidateRoot, "nested/file")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(historical.Body, body) {
		t.Fatal("history changed")
	}
	remote.wrongReceipt = true
	if _, err := writer.AddDirectory(t.Context(), removed.CandidateRoot, "other"); err == nil {
		t.Fatal("accepted wrong materialization receipt")
	}
}
func TestRootedRangeRejectsAuthenticatedIncorrectChunkLengths(t *testing.T) {
	remote := newTypedRemote(t)
	adapter, err := unixfs.NewAuthenticationAdapter(remote, remote.graph.Authentication(), maltcid.KZG4096)
	if err != nil {
		t.Fatal(err)
	}
	short, err := remote.Put(t.Context(), []byte("four"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := adapter.CreateMeasuredPayload(t.Context(), []cid.Cid{short, short}, 16, 8)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote, Layout: unixfs.LayoutRootedV1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadListPayloadRange(t.Context(), root, 0, 4); err == nil || !strings.Contains(err.Error(), "chunk length") {
		t.Fatalf("accepted incompatible payload: %v", err)
	}
}

func TestRootedPlannerAndAutomaticFilesystemReader(t *testing.T) {
	remote := newTypedRemote(t)
	layout, _ := unixfs.NewLayout(unixfs.LayoutRootedV1)
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: remote, Blocks: remote, Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := writer.EmptyDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := remote.Put(t.Context(), []byte("mounted body"))
	if err != nil {
		t.Fatal(err)
	}
	operations := []journal.Operation{}
	for i, intent := range []journal.Intent{{Kind: journal.KindMkdir, Path: "docs"}, {Kind: journal.KindWrite, Path: "docs/file", PayloadCID: payload.String()}} {
		intent.OperationID = fmt.Sprint("op", i)
		intent.RetryID = fmt.Sprint("retry", i)
		intent.DatasetID = "dataset"
		intent.Branch = "main"
		intent.BaseRoot = empty.CandidateRoot.String()
		operations = append(operations, journal.Operation{Intent: intent, Sequence: uint64(i + 1), Status: journal.StatusPendingUpload, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	}
	planner, err := unixfsclientroot.NewAuthentication(remote, remote, remote.graph.Authentication())
	if err != nil {
		t.Fatal(err)
	}
	candidates, required, root, err := planner.PlanRooted(t.Context(), empty.CandidateRoot, operations)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || len(required) != 1 || !required[0].Equals(payload) {
		t.Fatalf("wrong plan: %d %v", len(candidates), required)
	}
	for _, candidate := range candidates {
		if _, err := remote.MaterializeAuthentication(t.Context(), candidate); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	local, err := clientverifier.NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fs, err := filesystemservice.New(filesystemservice.Options{Reader: reader, Cache: store, Verifier: local})
	if err != nil {
		t.Fatal(err)
	}
	view := filesystemservice.View{DatasetID: "dataset", Branch: "main", Root: root}
	for i := 0; i < 2; i++ {
		body, _, err := fs.ReadFile(t.Context(), view, "docs/file")
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "mounted body" {
			t.Fatal("wrong file")
		}
	}
	if _, err := fs.Stat(t.Context(), view, "absent"); !errors.Is(err, unixfs.ErrNotFound) {
		t.Fatalf("absence: %v", err)
	}
	// A frozen write followed by unlink must return the exact original Root.
	operations = append(operations, journal.Operation{Intent: journal.Intent{OperationID: "delete", RetryID: "retry-delete", DatasetID: "dataset", Branch: "main", BaseRoot: empty.CandidateRoot.String(), Kind: journal.KindUnlink, Path: "docs/file"}, Sequence: 3, Status: journal.StatusPendingUpload, CreatedAt: time.Now(), UpdatedAt: time.Now()}, journal.Operation{Intent: journal.Intent{OperationID: "rmdir", RetryID: "retry-rmdir", DatasetID: "dataset", Branch: "main", BaseRoot: empty.CandidateRoot.String(), Kind: journal.KindUnlink, Path: "docs"}, Sequence: 4, Status: journal.StatusPendingUpload, CreatedAt: time.Now(), UpdatedAt: time.Now()})
	_, required, again, err := planner.PlanRooted(t.Context(), empty.CandidateRoot, operations)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Equals(empty.CandidateRoot) || len(required) != 0 {
		t.Fatal("net-zero plan changed state")
	}
}

func TestRootedWriterPreservesInjectedIPA(t *testing.T) {
	remote := newTypedRemote(t)
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.graph.Authentication().Profiles.Register(scheme); err != nil {
		t.Fatal(err)
	}
	adapter, err := unixfs.NewAuthenticationAdapter(remote, remote.graph.Authentication(), maltcid.IPA256)
	if err != nil {
		t.Fatal(err)
	}
	layout, _ := unixfs.NewLayout(unixfs.LayoutRootedV1)
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: remote, Blocks: remote, Layout: layout, Roots: adapter, Lists: adapter})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := writer.EmptyDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if maltcid.BackendKindOf(empty.CandidateRoot) != maltcid.BackendKindIPA {
		t.Fatal("injected IPA was replaced")
	}
	next, err := writer.AddFile(t.Context(), empty.CandidateRoot, "file", []byte("ipa"))
	if err != nil {
		t.Fatal(err)
	}
	if maltcid.BackendKindOf(next.CandidateRoot) != maltcid.BackendKindIPA {
		t.Fatal("update changed VC profile")
	}
	// The default adapter can update an existing IPA directory without migration.
	defaultWriter, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: remote, Blocks: remote, Layout: layout})
	if err != nil {
		t.Fatal(err)
	}
	removed, err := defaultWriter.RemovePath(t.Context(), next.CandidateRoot, "file")
	if err != nil {
		t.Fatal(err)
	}
	if !removed.CandidateRoot.Equals(empty.CandidateRoot) {
		t.Fatal("remove changed original descriptor")
	}
}
