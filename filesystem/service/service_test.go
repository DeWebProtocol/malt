package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/cache"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type fakeReader struct {
	stats      map[string]*unixfs.Stat
	bodies     map[string][]byte
	statCalls  int
	fullReads  int
	rangeReads int
	lookups    int
}

func (r *fakeReader) Resolve(context.Context, cid.Cid, string) (*unixfs.Resolution, error) {
	return nil, errors.New("unexpected direct resolve")
}

func (r *fakeReader) Stat(_ context.Context, _ cid.Cid, path string) (*unixfs.Stat, error) {
	r.statCalls++
	value, ok := r.stats[path]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	return cloneStat(value), nil
}

func (r *fakeReader) Lookup(_ context.Context, _ cid.Cid, path string) (*unixfs.Stat, error) {
	r.lookups++
	value, ok := r.stats[path]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	result := cloneStat(value)
	if result.Kind == unixfs.StagedKindFile {
		result.Size = 0
	}
	return result, nil
}

func (r *fakeReader) ReadFile(_ context.Context, _ cid.Cid, path string) (*unixfs.ReadResult, error) {
	r.fullReads++
	stat, ok := r.stats[path]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	body := append([]byte(nil), r.bodies[path]...)
	resolution := payloadResolution(stat)
	return &unixfs.ReadResult{
		Body: body, Target: stat.Payload, Offset: 0, End: uint64(len(body)),
		TotalSize: stat.Size, Resolution: &resolution,
	}, nil
}

func (r *fakeReader) ReadFileRange(_ context.Context, _ cid.Cid, path string, offset, length uint64) (*unixfs.ReadResult, error) {
	r.rangeReads++
	stat, ok := r.stats[path]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	body := r.bodies[path]
	end := saturatingAdd(offset, length)
	if end > uint64(len(body)) {
		end = uint64(len(body))
	}
	var selected []byte
	if offset < uint64(len(body)) && length > 0 {
		selected = append([]byte(nil), body[offset:end]...)
	}
	resolution := payloadResolution(stat)
	return &unixfs.ReadResult{
		Body: selected, Target: stat.Payload, Offset: offset, End: end,
		TotalSize: stat.Size, Resolution: &resolution,
	}, nil
}

func (r *fakeReader) ReadListPayloadRange(context.Context, cid.Cid, uint64, uint64) (*unixfs.ReadResult, error) {
	return nil, errors.New("unexpected direct list read")
}

type fakeVerifier struct {
	calls      int
	rejectNext bool
}

func (v *fakeVerifier) VerifyResolve(context.Context, protocol.ResolveVerification) error {
	v.calls++
	if v.rejectNext {
		v.rejectNext = false
		return errors.New("invalid cached proof")
	}
	return nil
}

func (*fakeVerifier) VerifyRead(context.Context, protocol.ReadVerification) error { return nil }

func TestStatReadDirAndOpenUseOneTransportNeutralService(t *testing.T) {
	view := testView(t)
	directoryRoot := testCID(t, []byte("directory root"))
	manifestCID := testCID(t, []byte("manifest"))
	reader := &fakeReader{stats: map[string]*unixfs.Stat{
		"docs": directoryStat(view.Root, "docs", directoryRoot, manifestCID, []unixfsmodel.DirectoryEntry{
			{Name: "z.txt", Type: unixfsmodel.DirectoryEntryTypeFile},
			{Name: "archive", Type: unixfsmodel.DirectoryEntryTypeDir},
			{Name: "legacy.txt", Type: unixfsmodel.DirectoryEntryTypeUnknown},
		}),
		"docs/legacy.txt": fileStat(view.Root, "docs/legacy.txt", testCID(t, []byte("legacy")), "raw", 6),
	}}
	service, err := New(Options{Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	info, err := service.Stat(t.Context(), view, "docs")
	if err != nil || !info.IsDir() || info.Path != "docs" || info.Payload != manifestCID {
		t.Fatalf("Stat = %#v, %v", info, err)
	}
	entries, err := service.ReadDir(t.Context(), view, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{entries[0].Name, entries[1].Name, entries[2].Name}; !slices.Equal(got, []string{"archive", "legacy.txt", "z.txt"}) {
		t.Fatalf("ReadDir names = %v", got)
	}
	if entries[1].Kind != unixfs.StagedKindFile {
		t.Fatalf("legacy entry kind = %q", entries[1].Kind)
	}
	if _, err := service.Open(t.Context(), view, "docs"); !errors.Is(err, unixfs.ErrNotFile) {
		t.Fatalf("Open directory error = %v", err)
	}
}

func TestRawReadCacheHitRevalidatesProofCIDAndExactView(t *testing.T) {
	view := testView(t)
	body := []byte("verified raw payload")
	payload := testCID(t, body)
	reader := &fakeReader{
		stats:  map[string]*unixfs.Stat{"docs/file.txt": fileStat(view.Root, "docs/file.txt", payload, "raw", uint64(len(body)))},
		bodies: map[string][]byte{"docs/file.txt": body},
	}
	cacheDirectory := t.TempDir()
	store, err := cache.Open(cacheDirectory)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fakeVerifier{}
	service, err := New(Options{
		Reader: reader, Cache: store, Verifier: verifier,
		Now: func() time.Time { return time.Date(2026, 8, 17, 1, 2, 3, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}

	got, _, err := service.ReadFileRange(t.Context(), view, "docs/file.txt", 2, 5)
	if err != nil || string(got) != string(body[2:7]) || reader.fullReads != 1 {
		t.Fatalf("first range = %q, reads=%d, err=%v", got, reader.fullReads, err)
	}
	info, err := service.Stat(t.Context(), view, "docs/file.txt")
	if err != nil || info.Size != uint64(len(body)) || reader.fullReads != 1 || verifier.calls != 1 {
		t.Fatalf("cached stat = %#v, reads=%d verify=%d err=%v", info, reader.fullReads, verifier.calls, err)
	}
	got, _, err = service.ReadFileRange(t.Context(), view, "docs/file.txt", 3, 4)
	if err != nil || string(got) != string(body[3:7]) || reader.fullReads != 1 || verifier.calls != 2 {
		t.Fatalf("cache range = %q, reads=%d verify=%d err=%v", got, reader.fullReads, verifier.calls, err)
	}

	corruptOnlyBlob(t, cacheDirectory, len(body))
	got, _, err = service.ReadFileRange(t.Context(), view, "docs/file.txt", 0, 4)
	if err != nil || string(got) != string(body[:4]) || reader.fullReads != 2 {
		t.Fatalf("corrupt-cache recovery = %q, reads=%d err=%v", got, reader.fullReads, err)
	}

	binding := cache.Binding{
		DatasetID: view.DatasetID, Branch: view.Branch, Root: view.Root,
		Revision: view.Revision, CID: payload, EncryptionEpoch: view.EncryptionEpoch,
	}
	if _, err := store.PutVerified(binding, body, cache.VerificationEvidence{
		Profile: cacheEvidenceProfile, Evidence: []byte{'{', '"', 0xff, '"', '}'}, VerifiedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	got, _, err = service.ReadFileRange(t.Context(), view, "docs/file.txt", 0, 3)
	if err != nil || string(got) != string(body[:3]) || reader.fullReads != 3 || verifier.calls != 2 {
		t.Fatalf("lossy-evidence recovery = %q, reads=%d verify=%d err=%v", got, reader.fullReads, verifier.calls, err)
	}

	verifier.rejectNext = true
	got, _, err = service.ReadFileRange(t.Context(), view, "docs/file.txt", 1, 3)
	if err != nil || string(got) != string(body[1:4]) || reader.fullReads != 4 {
		t.Fatalf("invalid-proof recovery = %q, reads=%d err=%v", got, reader.fullReads, err)
	}

	differentRevision := view
	differentRevision.Revision++
	got, _, err = service.ReadFileRange(t.Context(), differentRevision, "docs/file.txt", 0, 2)
	if err != nil || string(got) != string(body[:2]) || reader.fullReads != 5 {
		t.Fatalf("wrong-revision isolation = %q, reads=%d err=%v", got, reader.fullReads, err)
	}
}

func TestRawReadRejectsBytesThatDoNotMatchAuthenticatedCID(t *testing.T) {
	view := testView(t)
	want := []byte("good payload")
	payload := testCID(t, want)
	reader := &fakeReader{
		stats:  map[string]*unixfs.Stat{"file.txt": fileStat(view.Root, "file.txt", payload, "raw", uint64(len(want)))},
		bodies: map[string][]byte{"file.txt": []byte("evil payload")},
	}
	service, err := New(Options{Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := service.ReadFile(t.Context(), view, "file.txt")
	if got != nil || err == nil {
		t.Fatalf("corrupt raw read = %q, %v", got, err)
	}
}

func TestListRangeRemainsLazyAndDoesNotEnterRawCIDCache(t *testing.T) {
	view := testView(t)
	body := []byte("large logical list-backed file")
	listRoot := testCID(t, []byte("typed list root placeholder"))
	reader := &fakeReader{
		stats:  map[string]*unixfs.Stat{"large.bin": fileStat(view.Root, "large.bin", listRoot, "list", uint64(len(body)))},
		bodies: map[string][]byte{"large.bin": body},
	}
	store, err := cache.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{Reader: reader, Cache: store, Verifier: &fakeVerifier{}})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := service.ReadFileRange(t.Context(), view, "large.bin", 6, 7)
	if err != nil || string(got) != string(body[6:13]) {
		t.Fatalf("list range = %q, %v", got, err)
	}
	if reader.rangeReads != 1 || reader.fullReads != 0 {
		t.Fatalf("list calls range=%d full=%d", reader.rangeReads, reader.fullReads)
	}
	entries, err := store.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("list logical body entered raw cache: %#v, %v", entries, err)
	}
}

func TestSelectedRootMismatchAndClosedHandleFailClosed(t *testing.T) {
	view := testView(t)
	body := []byte("payload")
	payload := testCID(t, body)
	stat := fileStat(view.Root, "file.txt", payload, "raw", uint64(len(body)))
	stat.Resolution.Request.Root = testCID(t, []byte("observed remote head")).String()
	reader := &fakeReader{stats: map[string]*unixfs.Stat{"file.txt": stat}, bodies: map[string][]byte{"file.txt": body}}
	service, err := New(Options{Reader: reader})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stat(t.Context(), view, "file.txt"); err == nil {
		t.Fatal("filesystem accepted a result resolved under a different root")
	}

	reader.stats["file.txt"] = fileStat(view.Root, "file.txt", payload, "raw", uint64(len(body)))
	handle, err := service.Open(t.Context(), view, "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := handle.Read(t.Context(), 1, 3); err != nil || string(got) != "ayl" {
		t.Fatalf("handle read = %q, %v", got, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := handle.Read(t.Context(), 0, 1); got != nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("closed handle read = %q, %v", got, err)
	}
}

func testView(t *testing.T) View {
	t.Helper()
	return View{DatasetID: "bucket-one", Branch: "main", Root: testCID(t, []byte("accepted root")), Revision: 7, EncryptionEpoch: 2}
}

func fileStat(root cid.Cid, path string, payload cid.Cid, payloadKind string, size uint64) *unixfs.Stat {
	resolution := testResolution(root, unixfsPath(path), payload)
	return &unixfs.Stat{
		Kind: unixfs.StagedKindFile, NodeRoot: payload, Payload: payload,
		StorageKind: payloadKind, PayloadKind: payloadKind, Size: size, Resolution: resolution,
	}
}

func directoryStat(root cid.Cid, path string, node, payload cid.Cid, entries []unixfsmodel.DirectoryEntry) *unixfs.Stat {
	segments := unixfsPath(path)
	resolution := testResolution(root, segments, node)
	payloadResolution := testResolution(root, append(append([]string(nil), segments...), "@payload"), payload)
	return &unixfs.Stat{
		Kind: unixfs.StagedKindDirectory, NodeRoot: node, Payload: payload,
		StorageKind: "map", PayloadKind: "raw", Entries: entries,
		Resolution: resolution, PayloadBinding: &payloadResolution,
	}
}

func testResolution(root cid.Cid, segments []string, target cid.Cid) unixfs.Resolution {
	request := protocol.ResolveRequest{Profile: protocol.ResolveProfile, Root: root.String(), Segments: append([]string(nil), segments...)}
	result := protocol.ResolveResult{Profile: protocol.ResolveProfile, Target: target.String()}
	return unixfs.Resolution{Request: request, Result: result, Target: target}
}

func unixfsPath(value string) []string {
	segments, err := unixfsmodel.ParsePath(value)
	if err != nil {
		panic(err)
	}
	return segments
}

func testCID(t *testing.T, data []byte) cid.Cid {
	t.Helper()
	value, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}).Sum(data)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func cloneStat(value *unixfs.Stat) *unixfs.Stat {
	cloned := *value
	cloned.Entries = append([]unixfsmodel.DirectoryEntry(nil), value.Entries...)
	cloned.Resolution.Request.Segments = append([]string(nil), value.Resolution.Request.Segments...)
	if value.PayloadBinding != nil {
		binding := *value.PayloadBinding
		binding.Request.Segments = append([]string(nil), binding.Request.Segments...)
		cloned.PayloadBinding = &binding
	}
	return &cloned
}

func corruptOnlyBlob(t *testing.T, directory string, size int) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(directory, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache blob count = %d, want 1", len(entries))
	}
	corrupt := make([]byte, size)
	for index := range corrupt {
		corrupt[index] = 'x'
	}
	if err := os.WriteFile(filepath.Join(directory, "blobs", entries[0].Name()), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
}
