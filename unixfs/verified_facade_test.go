package unixfs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"

	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	unixfs "github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	materialmemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type realRemote struct {
	engine       *engine.Engine
	nodes        *materialmemory.Nodes
	candidates   map[string]protocol.AuthenticationCandidate
	blocks       *casmemory.Store
	reads        []protocol.AuthenticationRequest
	wrongReceipt bool
}

type countingWriterRemote struct {
	inner         *realRemote
	remoteCalls   int
	blockCalls    int
	rootCalls     int
	mutationCalls int
}

func (r *countingWriterRemote) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	r.blockCalls++
	return r.inner.Get(ctx, key)
}

func (r *countingWriterRemote) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	r.blockCalls++
	return r.inner.Put(ctx, data)
}

func (r *countingWriterRemote) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	r.blockCalls++
	return r.inner.PutWithCodec(ctx, data, codec)
}

func (r *countingWriterRemote) UpdateStagedRoot(ctx context.Context, previous cid.Cid, bindings map[string]string) (cid.Cid, error) {
	r.rootCalls++
	return r.inner.UpdateStagedRoot(ctx, previous, bindings)
}

func (r *countingWriterRemote) reset() {
	r.remoteCalls = 0
	r.blockCalls = 0
	r.rootCalls = 0
	r.mutationCalls = 0
}

func (r *countingWriterRemote) calls() int {
	return r.remoteCalls + r.blockCalls + r.rootCalls + r.mutationCalls
}

func newRealRemote(t *testing.T) *realRemote {
	t.Helper()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		t.Fatal(err)
	}
	return &realRemote{engine: engine.New(profiles), nodes: materialmemory.NewNodes(), candidates: map[string]protocol.AuthenticationCandidate{}, blocks: casmemory.New()}
}

func (r *realRemote) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	return r.blocks.Get(ctx, key)
}

func (r *realRemote) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return r.blocks.Put(ctx, data)
}

func (r *realRemote) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	return r.blocks.PutWithCodec(ctx, data, codec)
}

func (r *realRemote) UpdateStagedRoot(ctx context.Context, previous cid.Cid, bindings map[string]string) (cid.Cid, error) {
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.KZG4096}}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target, err := cid.Decode(bindings[name])
		if err != nil {
			return cid.Undef, err
		}
		selector := []byte(name)
		if name == "@payload" {
			selector = []byte("@payload")
		}
		state.Entries = append(state.Entries, engine.Entry{Label: selector, Target: target})
	}
	candidate, err := authentication.Prepare(ctx, r.engine, state)
	if err != nil {
		return cid.Undef, err
	}
	return r.MaterializeAuthentication(ctx, candidate)
}

func materializeTree(t *testing.T, remote *realRemote, files map[string][]byte, chunkSize int) cid.Cid {
	t.Helper()
	root := unixfs.NewStagedDirectory()
	for path, data := range files {
		payload, _, err := unixfs.MaterializeStagedFilePayload(t.Context(), remote, remote, bytes.NewReader(data), int64(len(data)), chunkSize)
		if err != nil {
			t.Fatalf("materialize payload %s: %v", path, err)
		}
		if err := unixfs.SetStagedFile(root, path, payload); err != nil {
			t.Fatal(err)
		}
	}
	result, err := materializeHybrid(t, remote, remote, root)
	if err != nil {
		t.Fatal(err)
	}
	return result.Key
}

func TestVerifiedReaderBindsDirectoryRawAndLargeListPayloads(t *testing.T) {
	remote := newRealRemote(t)
	large := bytes.Repeat([]byte("0123456789abcdef"), 1024)
	root := materializeTree(t, remote, map[string][]byte{
		"docs/small.txt": []byte("small payload"),
		"docs/large.bin": large,
	}, 64)
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}

	dir, err := reader.Stat(t.Context(), root, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if dir.Kind != unixfs.StagedKindDirectory || dir.PayloadKind != "raw" ||
		len(dir.Entries) != 2 || dir.PayloadBinding == nil {
		t.Fatalf("directory stat = %#v", dir)
	}

	countedBlocks := &countingBlocks{inner: remote, gets: make(map[string]int)}
	countedReader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: countedBlocks})
	if err != nil {
		t.Fatal(err)
	}
	lookupReader, ok := countedReader.(interface {
		Lookup(context.Context, cid.Cid, string) (*unixfs.Stat, error)
	})
	if !ok {
		t.Fatal("verified reader does not expose payload-lazy Lookup")
	}
	lookedUp, err := lookupReader.Lookup(t.Context(), root, "docs/small.txt")
	if err != nil {
		t.Fatal(err)
	}
	if lookedUp.Size != 0 || countedBlocks.gets[lookedUp.Payload.KeyString()] != 0 {
		t.Fatalf("Lookup materialized raw payload: stat=%#v gets=%d", lookedUp, countedBlocks.gets[lookedUp.Payload.KeyString()])
	}
	small, err := countedReader.ReadFile(t.Context(), root, "docs/small.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(small.Body) != "small payload" || small.Resolution == nil || small.Authentication != nil {
		t.Fatalf("raw read = %#v body=%q", small, small.Body)
	}
	if got := countedBlocks.gets[small.Target.KeyString()]; got != 1 {
		t.Fatalf("raw payload fetches = %d, want 1", got)
	}

	remote.reads = nil
	stat, err := reader.Stat(t.Context(), root, "docs/large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size != uint64(len(large)) || stat.ChunkSize != 64 || stat.StorageKind != "positional" {
		t.Fatalf("large stat = %#v", stat)
	}
	if len(remote.reads) != 1 || remote.reads[0].Operation != "binding" || remote.reads[0].Label == nil || string(*remote.reads[0].Label) != string(coordinate.EncodeIndex(math.MaxUint64)) {
		t.Fatalf("large stat did not use a bounded metadata query: %#v", remote.reads)
	}
	if stat.AuthenticationMetadata == nil || stat.AuthenticationMetadata.Result.Binding == nil {
		t.Fatal("metadata query omitted authenticated structural metadata")
	}

	remote.reads = nil
	ranged, err := reader.ReadFileRange(t.Context(), root, "docs/large.bin", 61, 131)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ranged.Body, large[61:192]) {
		t.Fatal("verified list range bytes differ")
	}
	if ranged.Resolution == nil || ranged.Authentication == nil || ranged.Authentication.Request.Root != ranged.Target.String() {
		t.Fatalf("resolve-to-read continuity was not retained: %#v", ranged)
	}
	if len(remote.reads) != 2 || remote.reads[1].Root != ranged.Resolution.Target.String() {
		t.Fatalf("range read was not issued against resolved list root: %#v", remote.reads)
	}
	full, err := reader.ReadFile(t.Context(), root, "docs/large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(full.Body, large) {
		t.Fatal("full list-backed body differs")
	}
}

func TestStagedPathProjectionRebuildsLayoutsWithoutReadingFilePayloads(t *testing.T) {
	for _, kind := range []unixfs.LayoutKind{unixfs.LayoutHybridV1, unixfs.LayoutFlatV1} {
		t.Run(string(kind), func(t *testing.T) {
			remote := newRealRemote(t)
			rawCID, err := remote.Put(t.Context(), []byte("retained raw payload"))
			if err != nil {
				t.Fatal(err)
			}
			largeBody := bytes.Repeat([]byte("retained-list-payload"), 32)
			listCID, _, err := unixfs.MaterializeStagedFilePayload(
				t.Context(),
				remote,
				remote,
				bytes.NewReader(largeBody),
				int64(len(largeBody)),
				32,
			)
			if err != nil {
				t.Fatal(err)
			}
			if unixfsmodel.StorageKindFromCID(listCID) != "positional" {
				t.Fatalf("large payload CID = %s, want List", listCID)
			}
			staged := unixfs.NewStagedDirectory()
			if err := unixfs.SetStagedFile(staged, "raw.txt", rawCID); err != nil {
				t.Fatal(err)
			}
			if err := unixfs.SetStagedFile(staged, "nested/list.bin", listCID); err != nil {
				t.Fatal(err)
			}
			layout, err := unixfs.NewLayout(kind)
			if err != nil {
				t.Fatal(err)
			}
			materialized, err := layout.Materialize(t.Context(), remote, remote, staged)
			if err != nil {
				t.Fatal(err)
			}
			manifestProjector, err := unixfs.NewStagedPathStatter(unixfs.ReaderOptions{
				Remote: remote,
				Blocks: remote,
			})
			if err != nil {
				t.Fatal(err)
			}
			rootStat, err := manifestProjector.StatStagedPath(t.Context(), materialized.Key.String(), "")
			if err != nil {
				t.Fatal(err)
			}
			nestedStat, err := manifestProjector.StatStagedPath(t.Context(), materialized.Key.String(), "nested")
			if err != nil {
				t.Fatal(err)
			}
			rootManifestCID, err := cid.Decode(rootStat.Payload)
			if err != nil {
				t.Fatal(err)
			}
			nestedManifestCID, err := cid.Decode(nestedStat.Payload)
			if err != nil {
				t.Fatal(err)
			}

			counted := &countingBlocks{inner: remote, gets: make(map[string]int)}
			remote.reads = nil
			projector, err := unixfs.NewStagedPathStatter(unixfs.ReaderOptions{
				Remote: remote,
				Blocks: counted,
			})
			if err != nil {
				t.Fatal(err)
			}
			rebuilt, err := unixfs.LoadStagedCurrentTree(
				t.Context(),
				projector,
				counted,
				materialized.Key.String(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := rebuilt.Children["raw.txt"]; got == nil || !got.Key.Equals(rawCID) {
				t.Fatalf("rebuilt raw file = %#v", got)
			}
			if got := rebuilt.Children["nested"].Children["list.bin"]; got == nil || !got.Key.Equals(listCID) {
				t.Fatalf("rebuilt List file = %#v", got)
			}
			if counted.gets[rawCID.KeyString()] != 0 || counted.gets[listCID.KeyString()] != 0 {
				t.Fatalf(
					"tree projection fetched retained file payloads: raw=%d list=%d",
					counted.gets[rawCID.KeyString()],
					counted.gets[listCID.KeyString()],
				)
			}
			if len(remote.reads) != 0 {
				t.Fatalf("tree projection issued %d List metadata reads", len(remote.reads))
			}
			for label, manifestCID := range map[string]cid.Cid{
				"root":   rootManifestCID,
				"nested": nestedManifestCID,
			} {
				if got := counted.gets[manifestCID.KeyString()]; got != 1 {
					t.Fatalf("%s manifest GETs = %d, want 1", label, got)
				}
			}
			totalGets := 0
			for _, count := range counted.gets {
				totalGets += count
			}
			if totalGets == 0 {
				t.Fatal("tree projection did not fetch and validate directory manifests")
			}
		})
	}
}

func TestStagedPathProjectionRejectsManifestBytesNotBoundToCID(t *testing.T) {
	remote := newRealRemote(t)
	root := materializeTree(t, remote, map[string][]byte{
		"keep.txt": []byte("retained"),
	}, 64)
	emptyRoot := materializeTree(t, remote, map[string][]byte{}, 64)

	honestProjector, err := unixfs.NewStagedPathStatter(unixfs.ReaderOptions{
		Remote: remote,
		Blocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	rootStat, err := honestProjector.StatStagedPath(t.Context(), root.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	emptyStat, err := honestProjector.StatStagedPath(t.Context(), emptyRoot.String(), "")
	if err != nil {
		t.Fatal(err)
	}
	rootManifestCID, err := cid.Decode(rootStat.Payload)
	if err != nil {
		t.Fatal(err)
	}
	emptyManifestCID, err := cid.Decode(emptyStat.Payload)
	if err != nil {
		t.Fatal(err)
	}
	emptyManifest, err := remote.Get(t.Context(), emptyManifestCID)
	if err != nil {
		t.Fatal(err)
	}
	rootManifest, err := remote.Get(t.Context(), rootManifestCID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(emptyManifest, rootManifest) {
		t.Fatal("attack fixture did not substitute different canonical manifest bytes")
	}

	attacker := substitutedBlocks{
		inner:       remote,
		replace:     rootManifestCID,
		replacement: emptyManifest,
	}
	projector, err := unixfs.NewStagedPathStatter(unixfs.ReaderOptions{
		Remote: remote,
		Blocks: attacker,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = unixfs.LoadStagedCurrentTree(t.Context(), projector, attacker, root.String())
	if err == nil || !strings.Contains(err.Error(), "directory manifest bytes do not match authenticated CID") {
		t.Fatalf("LoadStagedCurrentTree error = %v, want manifest CID mismatch", err)
	}
}

func TestVerifiedReaderUsesManifestTypeForMapBackedFile(t *testing.T) {
	remote := newRealRemote(t)
	payload := []byte("map-backed document")
	payloadCID, err := remote.Put(t.Context(), payload)
	if err != nil {
		t.Fatal(err)
	}
	commentCID, err := remote.Put(t.Context(), []byte("comment"))
	if err != nil {
		t.Fatal(err)
	}
	fileRoot, err := remote.UpdateStagedRoot(t.Context(), cid.Undef, map[string]string{
		"@payload":  payloadCID.String(),
		"@comments": commentCID.String(),
		"nested":    commentCID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := unixfs.NewStagedDirectory()
	if err := unixfs.SetStagedFile(staged, "report.docx", fileRoot); err != nil {
		t.Fatal(err)
	}
	materialized, err := materializeHybrid(t, remote, remote, staged)
	if err != nil {
		t.Fatal(err)
	}
	root := materialized.Key

	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	stat, err := reader.Stat(t.Context(), root, "report.docx")
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != unixfs.StagedKindFile || stat.StorageKind != "prefix" || stat.PayloadKind != "raw" {
		t.Fatalf("map-backed file stat = %#v", stat)
	}
	if !stat.NodeRoot.Equals(fileRoot) || !stat.Payload.Equals(payloadCID) || stat.PayloadBinding == nil {
		t.Fatalf("map-backed file binding = %#v", stat)
	}
	read, err := reader.ReadFile(t.Context(), root, "report.docx")
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Body) != string(payload) || !read.Target.Equals(payloadCID) {
		t.Fatalf("map-backed file read = %#v body=%q", read, read.Body)
	}
	if _, err := reader.Resolve(t.Context(), root, "report.docx/nested"); !errors.Is(err, unixfs.ErrNotDirectory) {
		t.Fatalf("traversal through file error = %v", err)
	}
}

func TestVerifiedReaderUsesDirectManifestCIDAsDirectory(t *testing.T) {
	remote := newRealRemote(t)
	fileCID, err := remote.Put(t.Context(), []byte("direct child"))
	if err != nil {
		t.Fatal(err)
	}
	directBlock, err := unixfsmodel.EncodeDirectoryManifest([]unixfsmodel.DirectoryEntry{
		{Name: "readme.txt", Type: unixfsmodel.DirectoryEntryTypeFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	directCID, err := remote.PutWithCodec(t.Context(), directBlock.Data, directBlock.Codec)
	if err != nil {
		t.Fatal(err)
	}
	rootBlock, err := unixfsmodel.EncodeDirectoryManifest([]unixfsmodel.DirectoryEntry{
		{Name: "direct", Type: unixfsmodel.DirectoryEntryTypeDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootManifestCID, err := remote.PutWithCodec(t.Context(), rootBlock.Data, rootBlock.Codec)
	if err != nil {
		t.Fatal(err)
	}
	root, err := remote.UpdateStagedRoot(t.Context(), cid.Undef, map[string]string{
		"@payload":          rootManifestCID.String(),
		"direct":            directCID.String(),
		"direct/readme.txt": fileCID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}

	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	stat, err := reader.Stat(t.Context(), root, "direct")
	if err != nil {
		t.Fatal(err)
	}
	if stat.Kind != unixfs.StagedKindDirectory ||
		stat.StorageKind != "raw" ||
		stat.PayloadKind != "raw" ||
		!stat.NodeRoot.Equals(directCID) ||
		!stat.Payload.Equals(directCID) ||
		stat.PayloadBinding != nil ||
		len(stat.Entries) != 1 ||
		stat.Entries[0].Name != "readme.txt" ||
		stat.Entries[0].Type != unixfsmodel.DirectoryEntryTypeFile {
		t.Fatalf("direct manifest directory stat = %#v", stat)
	}
	read, err := reader.ReadFile(t.Context(), root, "direct/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(read.Body) != "direct child" || !read.Target.Equals(fileCID) {
		t.Fatalf("direct manifest descendant read = %#v body=%q", read, read.Body)
	}
}

func TestVerifiedReaderRejectsUntypedDirectoryManifests(t *testing.T) {
	for _, codec := range []uint64{0x310001, cid.Raw} {
		t.Run(fmt.Sprint(codec), func(t *testing.T) {
			remote := newRealRemote(t)
			payload, err := remote.PutWithCodec(t.Context(), []byte(`{"entries":["legacy"]}`), codec)
			if err != nil {
				t.Fatal(err)
			}
			root, err := remote.UpdateStagedRoot(t.Context(), cid.Undef, map[string]string{"@payload": payload.String()})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Stat(t.Context(), root, "legacy"); err == nil {
				t.Fatal("inferred child type from retired directory manifest")
			}
		})
	}
}

func TestVerifiedReaderFetchesEachAuthenticatedRangeBlockOnce(t *testing.T) {
	remote := newRealRemote(t)
	chunk := []byte("0123456789abcdef")
	body := bytes.Repeat(chunk, 8)
	root := materializeTree(t, remote, map[string][]byte{"repeated.bin": body}, len(chunk))
	blocks := &countingBlocks{inner: remote, gets: make(map[string]int)}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: blocks})
	if err != nil {
		t.Fatal(err)
	}

	result, err := reader.ReadFileRange(t.Context(), root, "repeated.bin", 0, uint64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Body, body) {
		t.Fatal("verified repeated-chunk body differs")
	}
	if result.Authentication == nil || len(result.Authentication.Result.Range.Segments) < 2 {
		t.Fatalf("range fixture did not produce multiple segments: %#v", result.Authentication)
	}
	unique := make(map[string]struct{})
	for _, raw := range result.Authentication.Result.Range.Segments {
		unique[raw.Target.String()] = struct{}{}
	}
	if len(unique) >= len(result.Authentication.Result.Range.Segments) {
		t.Fatalf("range fixture did not reuse a chunk CID: %v", result.Authentication.Result.Range.Segments)
	}
	for raw := range unique {
		key, err := cid.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := blocks.gets[key.KeyString()]; got != 1 {
			t.Fatalf("authenticated range block %s fetched %d times, want 1", key, got)
		}
	}
}

func TestHybridMaterializationRejectsNonCanonicalChild(t *testing.T) {
	payload, err := cid.Parse("bafkqaaa")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		".", "..", "@payload", "\x00", " @payload", " file", "file ", "   ", "\u0085file", "file\ufeff",
	} {
		t.Run(name, func(t *testing.T) {
			remote := newRealRemote(t)
			root := unixfs.NewStagedDirectory()
			root.Children[name] = &unixfs.StagedNode{
				Kind: unixfs.StagedKindFile,
				Key:  payload,
			}
			if _, err := materializeHybrid(t, remote, remote, root); err == nil {
				t.Fatalf("hybrid materialization accepted non-canonical child %q", name)
			}
		})
	}
}

func TestVerifiedReaderRejectsAuthenticatedUnknownTargetCodec(t *testing.T) {
	remote := newRealRemote(t)
	digest, err := mh.Sum([]byte("not a commitment"), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	// The codec claims to be a map/KZG root, but the non-identity multihash
	// makes it an invalid typed root. A valid parent proof may authenticate this
	// opaque value, but the UnixFS runtime must not reinterpret it as raw bytes.
	unknown := cid.NewCidV1(0x300101, digest)
	root, err := remote.UpdateStagedRoot(t.Context(), cid.Undef, map[string]string{"file.txt": unknown.String()})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Stat(t.Context(), root, "file.txt"); err == nil {
		t.Fatal("stat accepted an authenticated target with an invalid typed-root encoding")
	}
	if _, err := reader.ReadFile(t.Context(), root, "file.txt"); err == nil {
		t.Fatal("read accepted an authenticated target with an invalid typed-root encoding")
	}
}

type tamperedRemote struct{ *realRemote }

func (r tamperedRemote) Authenticate(ctx context.Context, request protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	result, err := r.realRemote.Authenticate(ctx, request)
	if err != nil {
		return nil, err
	}
	wrong, _ := cid.Parse("bafkqaaa")
	result.Resolved = wrong.String()
	return result, nil
}

type corruptBlocks struct{ inner *realRemote }

func (b corruptBlocks) Get(context.Context, cid.Cid) ([]byte, error) { return []byte("corrupt"), nil }

type countingBlocks struct {
	inner *realRemote
	gets  map[string]int
}

func (b *countingBlocks) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	b.gets[key.KeyString()]++
	return b.inner.Get(ctx, key)
}

func (b *countingBlocks) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return b.inner.Put(ctx, data)
}

func (b *countingBlocks) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	return b.inner.PutWithCodec(ctx, data, codec)
}

type fixedRootCreator struct {
	root cid.Cid
}

func (c fixedRootCreator) UpdateStagedRoot(context.Context, cid.Cid, map[string]string) (cid.Cid, error) {
	return c.root, nil
}

type substitutedBlocks struct {
	inner       *realRemote
	replace     cid.Cid
	replacement []byte
}

func (b substitutedBlocks) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if key.Equals(b.replace) {
		return append([]byte(nil), b.replacement...), nil
	}
	return b.inner.Get(ctx, key)
}

type splicedReadRemote struct {
	*realRemote
	other cid.Cid
}

func (r splicedReadRemote) Authenticate(ctx context.Context, request protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	if request.Operation != "resolve" {
		request.Root = r.other.String()
	}
	return r.realRemote.Authenticate(ctx, request)
}

func TestVerifiedReaderRejectsTamperedResultAndPayloadBytes(t *testing.T) {
	remote := newRealRemote(t)
	root := materializeTree(t, remote, map[string][]byte{"file.txt": []byte("authentic")}, 64)
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: tamperedRemote{remote}, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadFile(t.Context(), root, "file.txt"); err == nil {
		t.Fatal("reader accepted a target not bound by the resolve ProofList")
	}

	reader, err = unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: corruptBlocks{inner: remote}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadFile(t.Context(), root, "file.txt"); err == nil {
		t.Fatal("reader accepted payload bytes that do not match the authenticated CID")
	}
}

func TestVerifiedReaderRejectsResolveToReadCrossRootSplice(t *testing.T) {
	remote := newRealRemote(t)
	firstRoot := materializeTree(t, remote, map[string][]byte{"file.bin": bytes.Repeat([]byte("first"), 40)}, 32)
	secondRoot := materializeTree(t, remote, map[string][]byte{"file.bin": bytes.Repeat([]byte("other"), 40)}, 32)
	baseReader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	other, err := baseReader.Resolve(t.Context(), secondRoot, "file.bin")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: splicedReadRemote{realRemote: remote, other: other.Target}, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadFileRange(t.Context(), firstRoot, "file.bin", 0, 10); err == nil {
		t.Fatal("reader accepted a valid list proof from an unrelated resolved root")
	}
}

func TestVerifiedWriterRemoveDoesNotReadRetainedFilePayloads(t *testing.T) {
	for _, kind := range []unixfs.LayoutKind{unixfs.LayoutHybridV1, unixfs.LayoutFlatV1} {
		t.Run(string(kind), func(t *testing.T) {
			remote := newRealRemote(t)
			rawCID, err := remote.Put(t.Context(), []byte("retained raw payload"))
			if err != nil {
				t.Fatal(err)
			}
			listBody := bytes.Repeat([]byte("retained-list-payload"), 32)
			listCID, _, err := unixfs.MaterializeStagedFilePayload(
				t.Context(),
				remote,
				remote,
				bytes.NewReader(listBody),
				int64(len(listBody)),
				32,
			)
			if err != nil {
				t.Fatal(err)
			}
			removedCID, err := remote.Put(t.Context(), []byte("removed payload"))
			if err != nil {
				t.Fatal(err)
			}
			staged := unixfs.NewStagedDirectory()
			if err := unixfs.SetStagedFile(staged, "retained/raw.txt", rawCID); err != nil {
				t.Fatal(err)
			}
			if err := unixfs.SetStagedFile(staged, "retained/list.bin", listCID); err != nil {
				t.Fatal(err)
			}
			if err := unixfs.SetStagedFile(staged, "remove.txt", removedCID); err != nil {
				t.Fatal(err)
			}
			layout, err := unixfs.NewLayout(kind)
			if err != nil {
				t.Fatal(err)
			}
			materialized, err := layout.Materialize(t.Context(), remote, remote, staged)
			if err != nil {
				t.Fatal(err)
			}

			counted := &countingBlocks{inner: remote, gets: make(map[string]int)}
			writer, err := unixfs.NewWriter(unixfs.WriterOptions{
				Remote: remote, Blocks: counted, Roots: remote, Layout: layout, ChunkSize: 32,
			})
			if err != nil {
				t.Fatal(err)
			}
			remote.reads = nil
			result, err := writer.RemovePath(t.Context(), materialized.Key, "remove.txt")
			if err != nil {
				t.Fatal(err)
			}
			if result.CandidateRoot.Equals(materialized.Key) {
				t.Fatal("remove did not change the candidate root")
			}
			if counted.gets[rawCID.KeyString()] != 0 || counted.gets[listCID.KeyString()] != 0 {
				t.Fatalf(
					"rm fetched retained file payloads: raw=%d list=%d",
					counted.gets[rawCID.KeyString()],
					counted.gets[listCID.KeyString()],
				)
			}
			if len(remote.reads) != 0 {
				t.Fatalf("rm issued %d retained List metadata reads", len(remote.reads))
			}
		})
	}
}

func TestVerifiedWriterRejectsCandidateWithWrongDirectoryProjection(t *testing.T) {
	for _, kind := range []unixfs.LayoutKind{unixfs.LayoutHybridV1, unixfs.LayoutFlatV1} {
		t.Run(string(kind), func(t *testing.T) {
			remote := newRealRemote(t)
			layout, err := unixfs.NewLayout(kind)
			if err != nil {
				t.Fatal(err)
			}
			baseTree := unixfs.NewStagedDirectory()
			keepCID, err := remote.Put(t.Context(), []byte("keep"))
			if err != nil {
				t.Fatal(err)
			}
			removeCID, err := remote.Put(t.Context(), []byte("remove"))
			if err != nil {
				t.Fatal(err)
			}
			if err := unixfs.SetStagedFile(baseTree, "keep.txt", keepCID); err != nil {
				t.Fatal(err)
			}
			if err := unixfs.SetStagedFile(baseTree, "remove.txt", removeCID); err != nil {
				t.Fatal(err)
			}
			base, err := layout.Materialize(t.Context(), remote, remote, baseTree)
			if err != nil {
				t.Fatal(err)
			}
			empty, err := layout.Materialize(t.Context(), remote, remote, unixfs.NewStagedDirectory())
			if err != nil {
				t.Fatal(err)
			}
			writer, err := unixfs.NewWriter(unixfs.WriterOptions{
				Remote:   remote,
				Blocks:   remote,
				Roots:    fixedRootCreator{root: empty.Key},
				Payloads: remote,
				Layout:   layout,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = writer.RemovePath(t.Context(), base.Key, "remove.txt")
			if err == nil || !strings.Contains(err.Error(), "entries do not match materialized manifest") {
				t.Fatalf("RemovePath error = %v, want candidate projection mismatch", err)
			}
		})
	}
}

func TestVerifiedWriterRemoveProducesUncheckedCandidateWithoutChangingBase(t *testing.T) {
	remote := newRealRemote(t)
	root := materializeTree(t, remote, map[string][]byte{
		"keep.txt":        []byte("keep"),
		"docs/remove.txt": []byte("remove"),
	}, 64)
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{
		Remote: remote,
		Blocks: remote,
		Roots:  remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := writer.RemovePath(t.Context(), root, "docs/remove.txt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted || !result.BaseRoot.Equals(root) || result.CandidateRoot.Equals(root) {
		t.Fatalf("remove result = %#v", result)
	}
	if _, err := writer.Stat(t.Context(), root, "docs/remove.txt"); err != nil {
		t.Fatalf("base root was mutated: %v", err)
	}
	if _, err := writer.Stat(t.Context(), result.CandidateRoot, "docs/remove.txt"); err == nil {
		t.Fatal("removed path still resolves from candidate root")
	}
	kept, err := writer.ReadFile(t.Context(), result.CandidateRoot, "keep.txt")
	if err != nil || string(kept.Body) != "keep" {
		t.Fatalf("candidate lost retained file: body=%q err=%v", keptBody(kept), err)
	}
}

func TestVerifiedWriterAddsDirectoryRawAndStreamedListWithoutTrustingCandidate(t *testing.T) {
	remote := newRealRemote(t)
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{
		Remote:    remote,
		Blocks:    remote,
		Roots:     remote,
		ChunkSize: 32,
	})
	if err != nil {
		t.Fatal(err)
	}

	empty, err := writer.EmptyDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if empty.Accepted || !empty.CandidateRoot.Defined() {
		t.Fatalf("empty result = %#v", empty)
	}
	directory, err := writer.AddDirectory(t.Context(), empty.CandidateRoot, "docs/generated")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Accepted || !directory.BaseRoot.Equals(empty.CandidateRoot) {
		t.Fatalf("directory result = %#v", directory)
	}
	raw, err := writer.AddFile(t.Context(), directory.CandidateRoot, "docs/readme.txt", []byte("verified raw"))
	if err != nil {
		t.Fatal(err)
	}
	largeBody := bytes.Repeat([]byte("streamed-list-"), 20)
	large, err := writer.AddFileSized(t.Context(), raw.CandidateRoot, "docs/large.bin", bytes.NewReader(largeBody), int64(len(largeBody)))
	if err != nil {
		t.Fatal(err)
	}
	if large.Accepted || large.Size != uint64(len(largeBody)) || large.CandidateRoot.Equals(raw.CandidateRoot) {
		t.Fatalf("large result = %#v", large)
	}
	read, err := writer.ReadFile(t.Context(), large.CandidateRoot, "docs/large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read.Body, largeBody) || read.ChunkSize != 32 {
		t.Fatalf("streamed body mismatch: size=%d chunk=%d", len(read.Body), read.ChunkSize)
	}
	if _, err := writer.Stat(t.Context(), large.CandidateRoot, "docs/generated"); err != nil {
		t.Fatalf("created directory missing from candidate: %v", err)
	}
	if _, err := writer.Stat(t.Context(), raw.CandidateRoot, "docs/large.bin"); err == nil {
		t.Fatal("base root was changed when streamed candidate was created")
	}
}

func TestVerifiedWriterRejectsMismatchedStreamSizeAndDirectoryReplacement(t *testing.T) {
	remote := newRealRemote(t)
	root := materializeTree(t, remote, map[string][]byte{"file": []byte("payload")}, 32)
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: remote, Blocks: remote, Roots: remote, ChunkSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AddFileSized(t.Context(), root, "other", bytes.NewReader([]byte("short")), 12); err == nil {
		t.Fatal("writer accepted a short stream")
	}
	if _, err := writer.AddFileSized(t.Context(), root, "other", bytes.NewReader([]byte("too long")), 3); err == nil {
		t.Fatal("writer accepted an overlong stream")
	}
	if _, err := writer.AddDirectory(t.Context(), root, "file/child"); err == nil {
		t.Fatal("writer replaced an existing file with a directory")
	}
}

func TestVerifiedWriterRejectsNonCanonicalPathsBeforeAnyIO(t *testing.T) {
	remote := newRealRemote(t)
	root := materializeTree(t, remote, map[string][]byte{"file": []byte("payload")}, 32)
	counting := &countingWriterRemote{inner: remote}
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{Remote: counting, Blocks: counting, Roots: counting, Payloads: counting, ChunkSize: 32})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "file collision after trimming", run: func() error {
			_, err := writer.AddFile(t.Context(), root, " file ", []byte("replacement"))
			return err
		}},
		{name: "directory trailing space", run: func() error {
			_, err := writer.AddDirectory(t.Context(), root, "dir ")
			return err
		}},
		{name: "streamed file leading space", run: func() error {
			_, err := writer.AddFileStream(t.Context(), root, " file", bytes.NewReader([]byte("replacement")))
			return err
		}},
		{name: "remove trailing space", run: func() error {
			_, err := writer.RemovePath(t.Context(), root, "file ")
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			counting.reset()
			if err := test.run(); err == nil {
				t.Fatal("writer accepted a non-canonical path")
			}
			if got := counting.calls(); got != 0 {
				t.Fatalf("non-canonical path performed I/O: remote=%d block=%d root=%d mutation=%d", counting.remoteCalls, counting.blockCalls, counting.rootCalls, counting.mutationCalls)
			}
		})
	}
}

func keptBody(result *unixfs.ReadResult) []byte {
	if result == nil {
		return nil
	}
	return result.Body
}

var _ unixfs.Remote = (*realRemote)(nil)
var _ unixfs.BlockStore = (*realRemote)(nil)
var _ unixfs.StagedRootWriter = (*realRemote)(nil)
var _ unixfs.MeasuredPayloadWriter = (*realRemote)(nil)
var _ unixfs.Remote = (*countingWriterRemote)(nil)
var _ unixfs.BlockStore = (*countingWriterRemote)(nil)
var _ unixfs.StagedRootWriter = (*countingWriterRemote)(nil)
var _ unixfs.MeasuredPayloadWriter = (*countingWriterRemote)(nil)

func (r *realRemote) CreateMeasuredPayload(ctx context.Context, chunks []cid.Cid, total, chunk uint64) (cid.Cid, error) {
	adapter, err := unixfs.NewAuthenticationAdapter(unixfs.LayoutHybridV1, r, r.engine, maltcid.KZG4096)
	if err != nil {
		return cid.Undef, err
	}
	return adapter.CreateMeasuredPayload(ctx, chunks, total, chunk)
}
func (r *countingWriterRemote) CreateMeasuredPayload(ctx context.Context, chunks []cid.Cid, total, chunk uint64) (cid.Cid, error) {
	r.mutationCalls++
	return r.inner.CreateMeasuredPayload(ctx, chunks, total, chunk)
}
func (r *countingWriterRemote) Authenticate(ctx context.Context, request protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	r.remoteCalls++
	return r.inner.Authenticate(ctx, request)
}

func materializeHybrid(t *testing.T, roots unixfs.StagedRootWriter, blocks unixfs.StagedBlockStore, node *unixfs.StagedNode) (*unixfs.StagedMaterializeResult, error) {
	t.Helper()
	layout, err := unixfs.NewLayout(unixfs.LayoutHybridV1)
	if err != nil {
		return nil, err
	}
	return layout.Materialize(t.Context(), roots, blocks, node)
}

func TestVerifiedReaderRejectsTamperedRangeBytes(t *testing.T) {
	remote := newRealRemote(t)
	body := bytes.Repeat([]byte("range-authentication"), 8)
	root, _, err := unixfs.MaterializeStagedFilePayload(t.Context(), remote, remote, bytes.NewReader(body), int64(len(body)), 32)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadPositionalPayloadRange(t.Context(), root, 5, 62)
	if err != nil || !bytes.Equal(result.Body, body[5:67]) {
		t.Fatalf("valid range: %v", err)
	}
	corrupted, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: corruptBlocks{inner: remote}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corrupted.ReadPositionalPayloadRange(t.Context(), root, 5, 62); err == nil {
		t.Fatal("accepted corrupted authenticated range segments")
	}
}
