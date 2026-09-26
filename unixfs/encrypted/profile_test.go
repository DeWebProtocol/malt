package encrypted_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	"github.com/dewebprotocol/malt-client/unixfs/encrypted"
	materialmemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

type profileRemote struct {
	engine *engine.Engine
	nodes  *materialmemory.Nodes
	blocks *casmemory.Store
	puts   [][]byte
	roots  []map[string]string
}

func newProfileRemote(t *testing.T) *profileRemote {
	return newProfileRemoteBackend(t, maltcid.BackendKindKZG)
}

func newProfileRemoteBackend(t *testing.T, backend maltcid.BackendKind) *profileRemote {
	t.Helper()
	profiles := engine.NewRegistry()
	switch backend {
	case maltcid.BackendKindKZG:
		scheme, err := kzg.NewScheme()
		if err != nil {
			t.Fatal(err)
		}
		if err := profiles.Register(scheme); err != nil {
			t.Fatal(err)
		}
	case maltcid.BackendKindIPA:
		scheme, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
		if err != nil {
			t.Fatal(err)
		}
		if err := profiles.Register(scheme); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("unsupported backend")
	}
	return &profileRemote{engine: engine.New(profiles), nodes: materialmemory.NewNodes(), blocks: casmemory.New()}
}

func TestEncryptedSnapshotPublishesExactIPARoots(t *testing.T) {
	remote := newProfileRemoteBackend(t, maltcid.BackendKindIPA)
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindIPA, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
		PlaintextChunkSize: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.bin"), bytes.Repeat([]byte{3}, 128), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{8, 8}
	binding, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Documents", PathName: "Documents",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSourceFingerprint, err := clientbackup.FingerprintSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if binding.SourceFingerprint != wantSourceFingerprint {
		t.Fatalf("encrypted source fingerprint = %q, want %q", binding.SourceFingerprint, wantSourceFingerprint)
	}
	built, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if maltcid.BackendKindOf(built.Root) != maltcid.BackendKindIPA {
		t.Fatalf("dataset backend = %s", maltcid.BackendKindOf(built.Root))
	}
}

func (r *profileRemote) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	return r.blocks.Get(ctx, key)
}

func (r *profileRemote) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	r.puts = append(r.puts, append([]byte(nil), data...))
	return r.blocks.Put(ctx, data)
}

func (r *profileRemote) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	r.puts = append(r.puts, append([]byte(nil), data...))
	return r.blocks.PutWithCodec(ctx, data, codec)
}

type failOnceBlockWriter struct {
	inner *profileRemote
	fail  bool
}

func (w *failOnceBlockWriter) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	if w.fail {
		w.fail = false
		return cid.Undef, errors.New("transient block publication failure")
	}
	return w.inner.Put(ctx, data)
}

func (w *failOnceBlockWriter) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if w.fail {
		w.fail = false
		return cid.Undef, errors.New("transient block publication failure")
	}
	return w.inner.PutWithCodec(ctx, data, codec)
}

func TestEncryptedProfileBuildReadRangeAndRestore(t *testing.T) {
	remote := newProfileRemote(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(source, "notes"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "notes", "todo.md"), []byte("private todo"), 0o640); err != nil {
		t.Fatal(err)
	}
	large := bytes.Repeat([]byte("0123456789abcdef"), 40)
	if err := os.WriteFile(filepath.Join(source, "large.bin"), large, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink("notes/todo.md", filepath.Join(source, "todo-link")); err != nil {
			t.Fatal(err)
		}
	}
	key := [32]byte{1, 3, 3, 7}
	local := casmemory.New()
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: local, RemoteGraph: remote, RemoteBlocks: remote,
		PlaintextChunkSize: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket-one", DatasetName: "Documents", Branch: "main",
		BindingID: "binding-docs", BindingName: "Docs", PathName: "Documents",
		Source: source, Epoch: 7, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket-one", PlanID: "plan-one", DatasetName: "Documents", Branch: "main", Epoch: 7, BucketKey: key, IndexKey: key,
		Bindings: []encrypted.PreparedBinding{prepared},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.puts) != 0 || len(remote.roots) != 0 {
		t.Fatal("local snapshot preparation performed remote I/O")
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, err := encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	resolveKey := func(epoch uint32) ([32]byte, error) {
		if epoch != 7 && epoch != encrypted.NamespaceKeyEpoch {
			return [32]byte{}, errors.New("unexpected epoch")
		}
		return key, nil
	}
	dataset, err := reader.LoadDataset(t.Context(), built.Root, "bucket-one", "main", resolveKey)
	if err != nil {
		t.Fatal(err)
	}
	rootDirectory, err := reader.OpenDirectory(t.Context(), dataset, "binding-docs", "", resolveKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryNames(rootDirectory.Manifest.Entries); !slicesEqual(got, []string{"empty", "large.bin", "notes", "todo-link"}) && runtime.GOOS != "windows" {
		t.Fatalf("root entries = %#v", got)
	} else if runtime.GOOS == "windows" && !slicesEqual(got, []string{"empty", "large.bin", "notes"}) {
		t.Fatalf("root entries = %#v", got)
	}
	body, err := reader.ReadFile(t.Context(), dataset, "binding-docs", "notes/todo.md", resolveKey)
	if err != nil || string(body) != "private todo" {
		t.Fatalf("small read = %q err=%v", body, err)
	}
	rangeBody, err := reader.ReadFileRange(t.Context(), dataset, "binding-docs", "large.bin", 55, 177, resolveKey)
	if err != nil || !bytes.Equal(rangeBody, large[55:232]) {
		t.Fatalf("range read length=%d err=%v", len(rangeBody), err)
	}
	empty, err := reader.ReadFile(t.Context(), dataset, "binding-docs", "empty", resolveKey)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty read = %q err=%v", empty, err)
	}
	destination := filepath.Join(t.TempDir(), "restore")
	if err := reader.RestoreBinding(t.Context(), dataset, "binding-docs", destination, resolveKey); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(destination, "large.bin"))
	if err != nil || !bytes.Equal(restored, large) {
		t.Fatalf("restored large length=%d err=%v", len(restored), err)
	}
	if runtime.GOOS != "windows" {
		link, err := os.Readlink(filepath.Join(destination, "todo-link"))
		if err != nil || link != "notes/todo.md" {
			t.Fatalf("restored symlink = %q err=%v", link, err)
		}
	}

	for _, stored := range remote.puts {
		for _, plaintext := range [][]byte{[]byte("todo.md"), []byte("private todo"), []byte("Documents")} {
			if bytes.Contains(stored, plaintext) {
				t.Fatalf("stored ciphertext exposes plaintext %q", plaintext)
			}
		}
	}
	for _, bindings := range remote.roots {
		for token := range bindings {
			if strings.HasPrefix(token, "@") {
				continue
			}
			if !strings.HasPrefix(token, "e1-") {
				t.Fatalf("MALT root exposes non-opaque child key %q", token)
			}
		}
	}
}

func TestEncryptedProfilePreservesWhitespaceInPathSegments(t *testing.T) {
	remote := newProfileRemote(t)
	source := filepath.Join(t.TempDir(), "source")
	directoryName := " notes "
	fileName := " todo.md "
	if err := os.MkdirAll(filepath.Join(source, directoryName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, directoryName, fileName), []byte("private todo"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{4, 2}
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Data", PathName: "   ",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{prepared},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, err := encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	resolveKey := func(epoch uint32) ([32]byte, error) {
		if epoch != 1 && epoch != encrypted.NamespaceKeyEpoch {
			return [32]byte{}, errors.New("unexpected epoch")
		}
		return key, nil
	}
	dataset, err := reader.LoadDataset(t.Context(), built.Root, "bucket", "main", resolveKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := reader.OpenDirectory(t.Context(), dataset, "binding", "", resolveKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryNames(root.Manifest.Entries); !slicesEqual(got, []string{directoryName}) {
		t.Fatalf("root entries = %#v", got)
	}
	relativePath := directoryName + "/" + fileName
	body, err := reader.ReadFile(t.Context(), dataset, "binding", relativePath, resolveKey)
	if err != nil || string(body) != "private todo" {
		t.Fatalf("whitespace path read = %q err=%v", body, err)
	}
	trimmedPath := strings.TrimSpace(directoryName) + "/" + strings.TrimSpace(fileName)
	if _, err := reader.ReadFile(t.Context(), dataset, "binding", trimmedPath, resolveKey); !errors.Is(err, encrypted.ErrNotFound) {
		t.Fatalf("trimmed path read error = %v, want not found", err)
	}
	working := t.TempDir()
	t.Chdir(working)
	destination := "   "
	if err := reader.RestoreBinding(t.Context(), dataset, "binding", destination, resolveKey); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(working, destination, directoryName, fileName))
	if err != nil || string(restored) != "private todo" {
		t.Fatalf("whitespace path restore = %q err=%v", restored, err)
	}
}

func TestEncryptedProfileKeepsOpaqueNamespaceStableAcrossContentKeyRotation(t *testing.T) {
	remote := newProfileRemote(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "private-name.md"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexKey := [32]byte{1, 1, 1}
	contentKeys := map[uint32][32]byte{
		encrypted.NamespaceKeyEpoch: indexKey,
		7:                           {7, 7, 7},
		8:                           {8, 8, 8},
	}
	build := func(epoch uint32) (encrypted.PreparedBinding, encrypted.DatasetBuildResult) {
		snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
			Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
		})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
			DatasetID: "bucket", DatasetName: "Data", Branch: "main",
			BindingID: "binding", BindingName: "Data", PathName: "Data", Source: source,
			Epoch: epoch, BucketKey: contentKeys[epoch], IndexKey: indexKey,
		})
		if err != nil {
			t.Fatal(err)
		}
		built, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
			DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
			Epoch: epoch, BucketKey: contentKeys[epoch], IndexKey: indexKey,
			Bindings: []encrypted.PreparedBinding{prepared},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshot.Publish(t.Context()); err != nil {
			t.Fatal(err)
		}
		return prepared, built
	}
	firstPrepared, firstBuilt := build(7)
	secondPrepared, secondBuilt := build(8)
	if firstPrepared.Manifest.Token != secondPrepared.Manifest.Token {
		t.Fatalf("binding token changed across content key rotation: %q != %q", firstPrepared.Manifest.Token, secondPrepared.Manifest.Token)
	}
	reader, err := encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	keys := func(epoch uint32) ([32]byte, error) {
		key, ok := contentKeys[epoch]
		if !ok {
			return [32]byte{}, errors.New("unknown epoch")
		}
		return key, nil
	}
	entryToken := func(root cid.Cid) string {
		dataset, err := reader.LoadDataset(t.Context(), root, "bucket", "main", keys)
		if err != nil {
			t.Fatal(err)
		}
		directory, err := reader.OpenDirectory(t.Context(), dataset, "binding", "", keys)
		if err != nil {
			t.Fatal(err)
		}
		return directory.Manifest.Entries[0].Token
	}
	if first, second := entryToken(firstBuilt.Root), entryToken(secondBuilt.Root); first != second {
		t.Fatalf("directory token changed across content key rotation: %q != %q", first, second)
	}
}

func TestEncryptedProfileRejectsReservedNamesAndEscapingSymlinks(t *testing.T) {
	remote := newProfileRemote(t)
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := [32]byte{4, 2}
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "@payload"), []byte("reserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main", BindingID: "binding", BindingName: "Data",
		PathName: "Data", Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	}
	if _, err := snapshot.PrepareBinding(t.Context(), request); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("reserved entry error = %v", err)
	}
	if err := os.Remove(filepath.Join(source, "@payload")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(filepath.Dir(source), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../outside", filepath.Join(source, "escape")); err != nil {
			t.Fatal(err)
		}
		if _, err := snapshot.PrepareBinding(t.Context(), request); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("escaping symlink error = %v", err)
		}
	}
	request.PathName = "../escape"
	if _, err := snapshot.PrepareBinding(t.Context(), request); err == nil {
		t.Fatal("escaping binding path name was accepted")
	}
}

func TestEncryptedProfileRejectsWrongKeyAndCorruptedCIDBody(t *testing.T) {
	remote := newProfileRemote(t)
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{9, 9, 9}
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main", BindingID: "binding", BindingName: "Data",
		PathName: "Data", Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main", Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{prepared},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, err := encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	wrong := func(uint32) ([32]byte, error) { return [32]byte{1}, nil }
	if _, err := reader.LoadDataset(t.Context(), built.Root, "bucket", "main", wrong); err == nil {
		t.Fatal("wrong key decrypted dataset manifest")
	}
	reader, err = encrypted.NewReader(encrypted.ReaderOptions{Remote: tamperedProfileRemote{remote}, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	resolveKey := func(uint32) ([32]byte, error) { return key, nil }
	if _, err := reader.LoadDataset(t.Context(), built.Root, "bucket", "main", resolveKey); err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("invalid proof error = %v", err)
	}

	tampered := &tamperedBlocks{profileRemote: remote, target: built.ManifestCID}
	reader, err = encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: tampered})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.LoadDataset(t.Context(), built.Root, "bucket", "main", resolveKey); err == nil || !strings.Contains(err.Error(), "CID") {
		t.Fatalf("corrupted manifest error = %v", err)
	}
}

type substitutingGraph struct {
	inner          *profileRemote
	substituteMap  bool
	substituteList bool
}

func TestEncryptedSnapshotRejectsRemoteMapAndListRootSubstitution(t *testing.T) {
	for _, test := range []struct {
		name           string
		substituteMap  bool
		substituteList bool
	}{
		{name: "map", substituteMap: true},
		{name: "list", substituteList: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := newProfileRemote(t)
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "value.bin"), bytes.Repeat([]byte{7}, 256), 0o600); err != nil {
				t.Fatal(err)
			}
			graph := &substitutingGraph{inner: remote, substituteMap: test.substituteMap, substituteList: test.substituteList}
			snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
				Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(),
				RemoteGraph: graph, RemoteBlocks: remote, PlaintextChunkSize: 32,
			})
			if err != nil {
				t.Fatal(err)
			}
			key := [32]byte{9, 8, 7}
			binding, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
				DatasetID: "bucket", DatasetName: "Data", Branch: "main",
				BindingID: "binding", BindingName: "Documents", PathName: "Documents",
				Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
				DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
				Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding},
			}); err != nil {
				t.Fatal(err)
			}
			if err := snapshot.Publish(t.Context()); err == nil || !strings.Contains(err.Error(), "substituted") {
				t.Fatalf("root substitution error = %v", err)
			}
		})
	}
}

func TestEncryptedSnapshotRejectsNonUTF8NameBeforeRemotePublication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows paths are Unicode")
	}
	remote := newProfileRemote(t)
	source := t.TempDir()
	invalidName := string([]byte{'b', 'a', 'd', '-', 0xff})
	if err := os.WriteFile(filepath.Join(source, invalidName), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := [32]byte{4, 2}
	if _, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Documents", PathName: "Documents",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	}); err == nil || !strings.Contains(err.Error(), "entry name") {
		t.Fatalf("invalid UTF-8 filename error = %v", err)
	}
	if len(remote.puts) != 0 || len(remote.roots) != 0 {
		t.Fatal("invalid UTF-8 snapshot performed remote publication")
	}
}

func TestEncryptedSnapshotDoesNotFollowEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink setup requires platform privileges on Windows")
	}
	remote := newProfileRemote(t)
	source := t.TempDir()
	external := filepath.Join(t.TempDir(), "runtime-secret")
	if err := os.WriteFile(external, []byte("must not be uploaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := [32]byte{1, 9}
	if _, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Documents", PathName: "Documents",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	}); err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("escaping symlink error = %v", err)
	}
	if len(remote.puts) != 0 || len(remote.roots) != 0 {
		t.Fatal("escaping symlink performed remote publication")
	}
}

func TestEncryptedSnapshotSealsOnFirstPublishAttemptAndRetriesExactTransaction(t *testing.T) {
	remote := newProfileRemote(t)
	blocks := &failOnceBlockWriter{inner: remote, fail: true}
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: blocks,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{5, 4, 3}
	binding, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Documents", PathName: "Documents",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Publish(t.Context()); err == nil || !strings.Contains(err.Error(), "transient block publication failure") {
		t.Fatalf("first publish error = %v", err)
	}
	if _, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{}); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("prepare after publication attempt error = %v", err)
	}
	if _, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding},
	}); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Fatalf("build after publication attempt error = %v", err)
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatalf("retry exact publication: %v", err)
	}
	putCount := len(remote.puts)
	rootCount := len(remote.roots)
	if putCount == 0 || rootCount == 0 {
		t.Fatalf("retry did not publish local transaction: puts=%d roots=%d", putCount, rootCount)
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatalf("idempotent published retry: %v", err)
	}
	if len(remote.puts) != putCount || len(remote.roots) != rootCount {
		t.Fatal("completed snapshot was published twice")
	}
}

func TestEncryptedSnapshotRejectsMismatchedVerifiedManifestReuse(t *testing.T) {
	remote := newProfileRemote(t)
	key := [32]byte{6, 2, 6}
	first, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	binding, err := first.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Documents", PathName: "Documents",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := first.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, err := encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	resolveKey := func(uint32) ([32]byte, error) { return key, nil }
	verified, err := reader.LoadDataset(t.Context(), built.Root, "bucket", "main", resolveKey)
	if err != nil {
		t.Fatal(err)
	}
	second, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "renamed", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding}, ReuseManifest: verified,
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched manifest reuse error = %v", err)
	}
}

func TestEncryptedDatasetViewRejectsForgeryAndOwnsReturnedCopies(t *testing.T) {
	remote := newProfileRemote(t)
	key := [32]byte{7, 1, 7}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("verified"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := snapshot.PrepareBinding(t.Context(), encrypted.BindingSource{
		DatasetID: "bucket", DatasetName: "Data", Branch: "main",
		BindingID: "binding", BindingName: "Documents", PathName: "Documents",
		Source: source, Epoch: 1, BucketKey: key, IndexKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	built, err := snapshot.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Publish(t.Context()); err != nil {
		t.Fatal(err)
	}
	reader, err := encrypted.NewReader(encrypted.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	resolveKey := func(uint32) ([32]byte, error) { return key, nil }
	view, err := reader.LoadDataset(t.Context(), built.Root, "bucket", "main", resolveKey)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := view.VerifiedManifest()
	if err != nil {
		t.Fatal(err)
	}
	manifest.DatasetID = "attacker"
	manifest.Bindings[0].Name = "attacker"
	copiedBinding, ok := view.Binding(binding.Manifest.ID)
	if !ok {
		t.Fatal("verified binding missing")
	}
	copiedBinding.Root = cid.MustParse("bafkqaaa")

	body, err := reader.ReadFile(t.Context(), view, "binding", "value.txt", resolveKey)
	if err != nil || string(body) != "verified" {
		t.Fatalf("read through mutated compatibility view = %q, %v", body, err)
	}
	second, err := encrypted.NewSnapshot(encrypted.SnapshotOptions{
		Backend: maltcid.BackendKindKZG, LocalBlocks: casmemory.New(), RemoteGraph: remote, RemoteBlocks: remote,
	})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := second.BuildDataset(t.Context(), encrypted.DatasetBuildRequest{
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Data", Branch: "main",
		Epoch: 1, BucketKey: key, IndexKey: key, Bindings: []encrypted.PreparedBinding{binding}, ReuseManifest: view,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.ManifestCID.Equals(built.ManifestCID) {
		t.Fatalf("reused manifest CID = %s, want %s", reused.ManifestCID, built.ManifestCID)
	}
	forged := &encrypted.DatasetView{}
	if _, err := reader.OpenDirectory(t.Context(), forged, "binding", "", resolveKey); err == nil || !strings.Contains(err.Error(), "not locally verified") {
		t.Fatalf("forged dataset view error = %v", err)
	}
}

type tamperedProfileRemote struct{ *profileRemote }

func (r tamperedProfileRemote) Authenticate(ctx context.Context, request protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	result, err := r.profileRemote.Authenticate(ctx, request)
	if err != nil {
		return nil, err
	}
	result.Resolved = "bafkqaaa"
	return result, nil
}

type tamperedBlocks struct {
	*profileRemote
	target cid.Cid
}

func (b *tamperedBlocks) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	body, err := b.profileRemote.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if key.Equals(b.target) && len(body) != 0 {
		body[0] ^= 0xff
	}
	return body, nil
}

func directoryNames(entries []encrypted.DirectoryEntry) []string {
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Name
	}
	return result
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (r *profileRemote) Authenticate(ctx context.Context, request protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	result, err := authentication.Execute(ctx, r.engine, request, r.nodes)
	return &result, err
}
func (r *profileRemote) MaterializeAuthentication(ctx context.Context, candidate protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := authentication.Materialize(ctx, r.engine, candidate, r.nodes); err != nil {
		return cid.Undef, err
	}
	if candidate.State.Descriptor.Layout == maltcid.Prefix {
		bindings := make(map[string]string)
		for _, entry := range candidate.State.Entries {
			label := string(entry.Label)
			if string(entry.Label) == "@payload" {
				label = "@payload"
			}
			bindings[label] = entry.Target.String()
		}
		r.roots = append(r.roots, bindings)
	}
	return cid.Decode(candidate.Root)
}
func (g *substitutingGraph) MaterializeAuthentication(ctx context.Context, candidate protocol.AuthenticationCandidate) (cid.Cid, error) {
	root, err := g.inner.MaterializeAuthentication(ctx, candidate)
	if err != nil {
		return cid.Undef, err
	}
	if candidate.State.Descriptor.Layout == maltcid.Prefix && g.substituteMap {
		g.substituteMap = false
		return cid.MustParse("bafkqaaa"), nil
	}
	if candidate.State.Descriptor.Layout == maltcid.Positional && g.substituteList {
		g.substituteList = false
		return cid.MustParse("bafkqaaa"), nil
	}
	return root, nil
}
