package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

func digest(body []byte) string { h := sha256.Sum256(body); return hex.EncodeToString(h[:]) }

func TestLocalFilesReadEveryPayloadChunkRangeAndAbsence(t *testing.T) {
	for _, backend := range []string{"kzg", "ipa"} {
		t.Run(backend, func(t *testing.T) {
			files := map[string][]byte{"dir/a": bytes.Repeat([]byte("abc"), 25), "dir/deep/b": []byte("real side payload"), "empty": {}}
			s, err := initialize(t.Context(), request{Backend: backend, ChunkBytes: 16, Files: files})
			if err != nil {
				t.Fatal(err)
			}
			for path, body := range files {
				got := s.execute(t.Context(), request{ID: path, Operation: "read", Root: s.root.String(), Path: path})
				if !got.Verified || got.Absent || got.Error != "" || got.BodySHA256 != digest(body) || got.ReturnedBytes != uint64(len(body)) || got.ProofBytes == 0 || got.Calls == 0 {
					t.Fatalf("read %s: %+v", path, got)
				}
			}
			for _, path := range []string{"missing", "dir/deep/missing"} {
				got := s.execute(t.Context(), request{ID: path, Operation: "read", Root: s.root.String(), Path: path})
				if !got.Verified || !got.Absent || got.Error != "" || got.ReturnedBytes != 0 || got.ProofBytes == 0 {
					t.Fatalf("absence: %+v", got)
				}
			}
			for _, limits := range [][2]uint64{{0, 1}, {15, 35}, {70, 100}} {
				offset, length := limits[0], limits[1]
				got := s.execute(t.Context(), request{ID: "range", Operation: "range", Root: s.root.String(), Path: "dir/a", Offset: &offset, Length: &length})
				end := min(offset+length, uint64(len(files["dir/a"])))
				if !got.Verified || got.Absent || got.Error != "" || got.BodySHA256 != digest(files["dir/a"][offset:end]) || got.ReturnedBytes != end-offset {
					t.Fatalf("range: %+v", got)
				}
			}
			got := s.execute(t.Context(), request{ID: "wrong-root", Operation: "read", Root: "bafkqaaa", Path: "dir/a"})
			if got.Verified || got.Error == "" || got.Calls != 0 {
				t.Fatal("changed selected root accepted", got)
			}
		})
	}
}

type corruptBlocks struct{ *sourceStore }

func (c corruptBlocks) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	body, err := c.sourceStore.Get(ctx, key)
	if len(body) > 0 {
		body[0] ^= 0xff
	}
	return body, err
}

func TestLocalFileClientRejectsWrongPayloadBytes(t *testing.T) {
	s, err := initialize(t.Context(), request{Backend: "ipa", ChunkBytes: 8, Files: map[string][]byte{"a": bytes.Repeat([]byte("x"), 25)}})
	if err != nil {
		t.Fatal(err)
	}
	s.reader, err = unixfs.NewReader(unixfs.ReaderOptions{Layout: unixfs.LayoutRootedV1, Remote: s.store, Blocks: corruptBlocks{s.store}, Verifier: s.store.e})
	if err != nil {
		t.Fatal(err)
	}
	got := s.execute(t.Context(), request{ID: "corrupt", Operation: "read", Root: s.root.String(), Path: "a"})
	if got.Verified || got.Absent || got.Error == "" {
		t.Fatal("corrupted CAS bytes accepted or misclassified absent", got)
	}
}

func TestLocalFileProtocolRejectsIgnoredFieldsAndPartialLifecycle(t *testing.T) {
	for _, body := range []string{
		`{"schema":"malt-local-file-request/v1","id":"x","operation":"close","backend":"ipa"}`,
		`{"schema":"malt-local-file-request/v1","id":"x","operation":"close","id":"y"}`,
		`{"schema":"malt-local-file-request/v1","id":"x","operation":"range","root":"r","path":"a","offset":0,"length":null}`,
		`{"schema":"malt-local-file-request/v1","id":"x","operation":"init","backend":"ipa","chunk_bytes":8,"files":{"a":"eA==","a":"eQ=="}}`,
	} {
		if _, err := decode([]byte(body)); err == nil {
			t.Fatal("invalid protocol accepted", body)
		}
	}
	if _, err := initialize(t.Context(), request{Backend: "ipa", ChunkBytes: 8, Files: map[string][]byte{"a": {}, "a/b": {}}}); err == nil {
		t.Fatal("source file/directory collision accepted")
	}
	var input, output bytes.Buffer
	enc := json.NewEncoder(&input)
	enc.Encode(request{Schema: requestSchema, ID: "init", Operation: "init", Backend: "ipa", ChunkBytes: 8, Files: map[string][]byte{"a": []byte("content")}})
	if err := serve(t.Context(), &input, &output); err == nil || !strings.Contains(err.Error(), "close") {
		t.Fatal("incomplete lifecycle accepted", err)
	}
}
