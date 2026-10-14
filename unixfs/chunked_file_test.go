package unixfs

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-client/nodeapi"
	model "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

type fileCapture struct {
	state    engine.State
	bodies   [][]byte
	metadata []byte
	corrupt  bool
	builds   int
	engine   *engine.Engine
}

func (f *fileCapture) PutFileBlock(_ context.Context, data []byte, metadata bool) (cid.Cid, error) {
	if f.corrupt {
		return cid.MustParse("bafkqaaa"), nil
	}
	if metadata {
		f.metadata = append([]byte{}, data...)
	} else {
		f.bodies = append(f.bodies, append([]byte{}, data...))
	}
	return nodeapi.CIDForBlock(nodeapi.Block{Data: data, Codec: cid.Raw})
}
func (f *fileCapture) BuildAuthentication(ctx context.Context, state engine.State) (cid.Cid, error) {
	f.state = state
	f.builds++
	w, err := authentication.BuildWriter(ctx, f.engine, state)
	if err != nil {
		return cid.Undef, err
	}
	return w.Root(), nil
}

func TestChunkedFileUsesRuntimeGeometryAndChecksCASReceipts(t *testing.T) {
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileDirect)
	if err != nil {
		t.Fatal(err)
	}
	registry := engine.NewRegistry()
	if err := registry.Register(scheme); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, []byte("123456789")} {
		capture := &fileCapture{engine: engine.New(registry)}
		root, err := WriteChunkedFile(t.Context(), capture, capture, data, 0o755, 4, maltcid.IPA256)
		if err != nil || !maltcid.IsMaltCid(root) {
			t.Fatal(err)
		}
		count := (len(data) + 3) / 4
		if len(capture.bodies) != count || len(capture.state.Entries) != count {
			t.Fatal("chunk count")
		}
		metadata, err := model.ParseChunkMetadata(capture.state.PayloadCID, capture.metadata, uint64(count))
		if err != nil || metadata.TotalSize != uint64(len(data)) || metadata.Mode == nil || *metadata.Mode != 0o755 {
			t.Fatal("metadata", err)
		}
		bad := &fileCapture{corrupt: true}
		if _, err := WriteChunkedFile(t.Context(), bad, bad, data, 0o755, 4, maltcid.IPA256); !errors.Is(err, nodeapi.ErrCorruptedBlock) || bad.builds != 0 {
			t.Fatal("unbound CAS receipt accepted", err)
		}
	}
}

func TestFlatProjectionEscapesOnlyTheLeadingReservedNamespace(t *testing.T) {
	a := cid.MustParse("bafkqaaa")
	bindings := ProjectDirectoryBindings(LayoutFlatV1, []DirectoryChild{{Name: "@payload", Root: a}, {Name: "src", Directory: true, Manifest: a, Descendants: map[string]cid.Cid{"/@scope/file": a}}})
	if !bindings["/@payload"].Equals(a) || !bindings["src/@scope/file"].Equals(a) || !bindings["src"].Equals(a) || len(bindings) != 3 {
		t.Fatal(bindings)
	}
}
