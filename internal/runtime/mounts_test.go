package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	truststore "github.com/dewebprotocol/malt-client/trust"
	cid "github.com/ipfs/go-cid"
)

type fakeTrustStates struct {
	states map[string]truststore.RootState
}

func (f fakeTrustStates) GetState(alias string) (truststore.RootState, error) {
	state, ok := f.states[alias]
	if !ok {
		return truststore.RootState{}, truststore.ErrNotFound
	}
	return state, nil
}

func TestAcceptedViewSelectorNeverUsesCandidateOrUnmatchedObservation(t *testing.T) {
	accepted := runtimeTestCID(t, "accepted")
	other := runtimeTestCID(t, "other")
	selector := acceptedViewSelector{
		remoteSource: "https://gateway.example",
		trust: fakeTrustStates{states: map[string]truststore.RootState{
			"docs": {
				Alias: "docs", Profile: "unixfs",
				Accepted:   &truststore.AcceptedRootState{Root: accepted.String(), AcceptedAt: time.Now()},
				Candidates: []truststore.CandidateRoot{{Root: other.String(), BaseRoot: accepted.String()}},
				ObservedHeads: []truststore.ObservedHead{
					{Source: "https://gateway.example", DatasetID: "bucket-one", Branch: "main", Root: accepted.String(), Revision: 7, ObservedAt: time.Now()},
					{Source: "https://gateway.example", DatasetID: "bucket-one", Branch: "main", Root: other.String(), Revision: 99, ObservedAt: time.Now()},
					{Source: "https://other.example", DatasetID: "bucket-one", Branch: "main", Root: accepted.String(), Revision: 100, ObservedAt: time.Now()},
				},
			},
		}},
	}
	spec := filesystemmount.Spec{DatasetID: "bucket-one", Branch: "main", TrustAlias: "docs"}
	view, err := selector.SelectView(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Root.Equals(accepted) || view.Revision != 7 || view.DatasetID != spec.DatasetID || view.Branch != spec.Branch {
		t.Fatalf("selected View=%#v", view)
	}
	if view.Root.Equals(other) {
		t.Fatal("candidate or unmatched observation became mount authority")
	}
}

func TestAcceptedViewSelectorFailsClosed(t *testing.T) {
	accepted := runtimeTestCID(t, "accepted")
	base := truststore.RootState{
		Alias: "docs", Profile: "unixfs",
		Accepted: &truststore.AcceptedRootState{Root: accepted.String(), AcceptedAt: time.Now()},
	}
	for _, test := range []struct {
		name  string
		state truststore.RootState
		spec  filesystemmount.Spec
		want  error
	}{
		{name: "encrypted", state: base, spec: filesystemmount.Spec{TrustAlias: "docs", DatasetID: "bucket", Branch: "main", EncryptionEpoch: 1}, want: ErrEncryptedMountUnavailable},
		{name: "wrong profile", state: func() truststore.RootState { value := base; value.Profile = "map"; return value }(), spec: filesystemmount.Spec{TrustAlias: "docs", DatasetID: "bucket", Branch: "main"}},
		{name: "no accepted", state: truststore.RootState{Alias: "docs", Profile: "unixfs"}, spec: filesystemmount.Spec{TrustAlias: "docs", DatasetID: "bucket", Branch: "main"}, want: truststore.ErrNoAcceptedRoot},
	} {
		t.Run(test.name, func(t *testing.T) {
			selector := acceptedViewSelector{trust: fakeTrustStates{states: map[string]truststore.RootState{"docs": test.state}}}
			if _, err := selector.SelectView(t.Context(), test.spec); err == nil || (test.want != nil && !errors.Is(err, test.want)) {
				t.Fatalf("SelectView error=%v, want %v", err, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	selector := acceptedViewSelector{trust: fakeTrustStates{states: map[string]truststore.RootState{"docs": base}}}
	if _, err := selector.SelectView(ctx, filesystemmount.Spec{TrustAlias: "docs"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled SelectView error=%v", err)
	}
}

type fakeViewFilesystem struct {
	id string
}

func (f *fakeViewFilesystem) Stat(_ context.Context, _ filesystemservice.View, path string) (filesystemservice.Info, error) {
	return filesystemservice.Info{Name: f.id + ":" + path}, nil
}

func (f *fakeViewFilesystem) ReadDir(context.Context, filesystemservice.View, string) ([]filesystemservice.DirEntry, error) {
	return []filesystemservice.DirEntry{{Name: f.id}}, nil
}

func (f *fakeViewFilesystem) Open(context.Context, filesystemservice.View, string) (*filesystemservice.Handle, error) {
	return nil, fmt.Errorf("not used")
}

func TestGatewayFilesystemRouterKeysServicesByDatasetAndBranch(t *testing.T) {
	var mu sync.Mutex
	calls := map[datasetBranch]int{}
	router, err := newGatewayFilesystemRouter(func(dataset, branch string) (filesystemmount.ViewFilesystem, error) {
		mu.Lock()
		defer mu.Unlock()
		key := datasetBranch{dataset: dataset, branch: branch}
		calls[key]++
		return &fakeViewFilesystem{id: dataset + "/" + branch}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []filesystemservice.View{
		{DatasetID: "one", Branch: "main"},
		{DatasetID: "one", Branch: "main"},
		{DatasetID: "one", Branch: "feature"},
		{DatasetID: "two", Branch: "main"},
	} {
		info, err := router.Stat(t.Context(), view, "docs")
		if err != nil || info.Name != view.DatasetID+"/"+view.Branch+":docs" {
			t.Fatalf("Stat view=%#v info=%#v err=%v", view, info, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 || calls[datasetBranch{dataset: "one", branch: "main"}] != 1 {
		t.Fatalf("filesystem factory calls=%v", calls)
	}
}

func TestGatewayFilesystemRouterBindsOnlyExactWriteBackSpecAndView(t *testing.T) {
	wantErr := errors.New("binding sentinel")
	var gotSpec filesystemmount.Spec
	var gotView filesystemservice.View
	var gotFilesystem filesystemmount.ViewFilesystem
	service := &fakeViewFilesystem{id: "verified"}
	router, err := newGatewayFilesystemRouter(
		func(string, string) (filesystemmount.ViewFilesystem, error) { return service, nil },
		func(_ context.Context, spec filesystemmount.Spec, view filesystemservice.View, filesystem filesystemmount.ViewFilesystem) (filesystemmount.WritableBinding, error) {
			gotSpec, gotView, gotFilesystem = spec, view, filesystem
			return nil, wantErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	spec := filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket", Branch: "main", Mountpoint: "/mnt/docs", TrustAlias: "docs",
		CachePolicy: filesystemmount.CacheVerified, WritePolicy: filesystemmount.WriteBack,
		LayoutPolicy: filesystemmount.LayoutFlatV1, ConflictPolicy: filesystemmount.ConflictPreserveLocal,
	}
	view := filesystemservice.View{DatasetID: "bucket", Branch: "main", Root: runtimeTestCID(t, "accepted")}
	if _, err := router.BindWritable(t.Context(), spec, view); !errors.Is(err, wantErr) {
		t.Fatalf("BindWritable error=%v", err)
	}
	if gotSpec != spec || gotView != view || gotFilesystem != service {
		t.Fatalf("binding inputs spec=%#v view=%#v filesystem=%T", gotSpec, gotView, gotFilesystem)
	}
	partial := &runtimeWritableBinding{}
	partialRouter, err := newGatewayFilesystemRouter(
		func(string, string) (filesystemmount.ViewFilesystem, error) { return service, nil },
		func(context.Context, filesystemmount.Spec, filesystemservice.View, filesystemmount.ViewFilesystem) (filesystemmount.WritableBinding, error) {
			return partial, wantErr
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	gotPartial, err := partialRouter.BindWritable(t.Context(), spec, view)
	if !errors.Is(err, wantErr) || gotPartial != partial {
		t.Fatalf("partial BindWritable binding=%T err=%v", gotPartial, err)
	}

	mismatched := view
	mismatched.Branch = "feature"
	if _, err := router.BindWritable(t.Context(), spec, mismatched); err == nil {
		t.Fatal("mismatched write-back Spec and View were accepted")
	}
	readOnly := spec
	readOnly.WritePolicy = filesystemmount.WriteReadOnly
	readOnly.LayoutPolicy = ""
	readOnly.ConflictPolicy = filesystemmount.ConflictFailReadOnly
	if _, err := router.BindWritable(t.Context(), readOnly, view); err == nil {
		t.Fatal("read-only Spec received a writable binding")
	}

	withoutBinding, err := newGatewayFilesystemRouter(func(string, string) (filesystemmount.ViewFilesystem, error) { return service, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withoutBinding.BindWritable(t.Context(), spec, view); err == nil {
		t.Fatal("router without binding capability accepted write-back")
	}

	var nilFilesystem *fakeViewFilesystem
	typedNilRouter, err := newGatewayFilesystemRouter(func(string, string) (filesystemmount.ViewFilesystem, error) {
		return nilFilesystem, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := typedNilRouter.Stat(t.Context(), view, ""); err == nil {
		t.Fatal("typed-nil verified filesystem was accepted")
	}
	typedNilBinding, err := newGatewayFilesystemRouter(
		func(string, string) (filesystemmount.ViewFilesystem, error) { return service, nil },
		func(context.Context, filesystemmount.Spec, filesystemservice.View, filesystemmount.ViewFilesystem) (filesystemmount.WritableBinding, error) {
			return (*runtimeWritableBinding)(nil), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := typedNilBinding.BindWritable(t.Context(), spec, view); err == nil {
		t.Fatal("typed-nil writable binding was accepted")
	}
}

func runtimeTestCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	key, err := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: -1}.Sum([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return key
}
