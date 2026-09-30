// malt-eval-local-files measures the real verified UnixFS facade over bounded
// in-memory authentication and payload capabilities. It is not a Gateway API.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"

	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

const requestSchema = "malt-local-file-request/v1"
const responseSchema = "malt-local-file-response/v1"
const maxLine = 64 << 20

type request struct {
	Schema     string            `json:"schema"`
	ID         string            `json:"id"`
	Operation  string            `json:"operation"`
	Backend    string            `json:"backend,omitempty"`
	ChunkBytes int               `json:"chunk_bytes,omitempty"`
	Files      map[string][]byte `json:"files,omitempty"`
	Root       string            `json:"root,omitempty"`
	Path       string            `json:"path,omitempty"`
	Offset     *uint64           `json:"offset,omitempty"`
	Length     *uint64           `json:"length,omitempty"`
}

type response struct {
	Schema        string `json:"schema"`
	ID            string `json:"id"`
	Root          string `json:"root"`
	Verified      bool   `json:"verified"`
	Absent        bool   `json:"absent"`
	BodySHA256    string `json:"body_sha256"`
	ReturnedBytes uint64 `json:"returned_bytes"`
	ProofBytes    uint64 `json:"proof_bytes"`
	CASBytes      uint64 `json:"cas_bytes"`
	Calls         uint64 `json:"calls"`
	Error         string `json:"error"`
}

type sourceStore struct {
	*casmemory.Store
	e                           *engine.Engine
	nodes                       *memory.Nodes
	candidates                  map[string]protocol.AuthenticationCandidate
	proofBytes, casBytes, calls uint64
}

func (s *sourceStore) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	s.calls++
	body, err := s.Store.Get(ctx, key)
	s.casBytes += uint64(len(body))
	return body, err
}
func (s *sourceStore) Authenticate(ctx context.Context, q protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error) {
	s.calls++
	result, err := authentication.Execute(ctx, s.e, q, s.nodes)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	s.proofBytes += uint64(len(body))
	// The verified client consumes decoded untrusted evidence, not the server's
	// internal object or a pre-supplied final CID.
	var decoded protocol.AuthenticationResult
	if err := strictjson.Decode(body, &decoded); err != nil {
		return nil, err
	}
	return &decoded, nil
}
func (s *sourceStore) MaterializeAuthentication(ctx context.Context, candidate protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := authentication.Materialize(ctx, s.e, candidate, s.nodes); err != nil {
		return cid.Undef, err
	}
	root, err := cid.Decode(candidate.Root)
	if err == nil {
		s.candidates[root.KeyString()] = candidate
	}
	return root, err
}
func (s *sourceStore) AuthenticationCandidate(ctx context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	candidate, ok := s.candidates[root.KeyString()]
	if !ok {
		return nil, fmt.Errorf("source candidate absent")
	}
	out, err := authentication.Export(ctx, s.e, root, candidate.State, s.nodes)
	return &out, err
}

type session struct {
	root   cid.Cid
	store  *sourceStore
	reader unixfs.Reader
}

func initialize(ctx context.Context, q request) (*session, error) {
	if q.Files == nil || len(q.Files) > 100_000 || q.ChunkBytes < 1 || q.ChunkBytes > 4<<20 {
		return nil, fmt.Errorf("explicit files and chunk size within 1..4MiB required")
	}
	names := make([]string, 0, len(q.Files))
	total := uint64(0)
	for name, body := range q.Files {
		parts, err := unixfs.ParseCanonicalStagedPath(name)
		if err != nil || len(parts) == 0 || strings.Join(parts, "/") != name {
			return nil, fmt.Errorf("invalid canonical source path %q", name)
		}
		for i := 1; i < len(parts); i++ {
			if _, exists := q.Files[strings.Join(parts[:i], "/")]; exists {
				return nil, fmt.Errorf("file/directory source collision")
			}
		}
		total += uint64(len(body))
		if total > 32<<20 {
			return nil, fmt.Errorf("source payload exceeds 32MiB")
		}
		names = append(names, name)
	}
	registry := engine.NewRegistry()
	profile := maltcid.KZG4096
	if q.Backend == "kzg" {
		scheme, err := kzg.NewScheme()
		if err != nil {
			return nil, err
		}
		if err = registry.Register(scheme); err != nil {
			return nil, err
		}
	} else if q.Backend == "ipa" {
		profile = maltcid.IPA256
		scheme, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
		if err != nil {
			return nil, err
		}
		if err = registry.Register(scheme); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("unknown backend")
	}
	store := &sourceStore{Store: casmemory.New(), e: engine.New(registry), nodes: memory.NewNodes(), candidates: map[string]protocol.AuthenticationCandidate{}}
	adapter, err := unixfs.NewAuthenticationAdapter(unixfs.LayoutRootedV1, store, store.e, profile)
	if err != nil {
		return nil, err
	}
	tree := unixfs.NewStagedDirectory()
	tree.Changed = true
	sort.Strings(names)
	for _, name := range names {
		body := q.Files[name]
		target, _, err := unixfs.MaterializeStagedFilePayload(ctx, store, adapter, bytes.NewReader(body), int64(len(body)), q.ChunkBytes)
		if err != nil {
			return nil, err
		}
		if err := unixfs.SetStagedFile(tree, name, target); err != nil {
			return nil, err
		}
	}
	layout, err := unixfs.NewLayout(unixfs.LayoutRootedV1)
	if err != nil {
		return nil, err
	}
	result, err := layout.Materialize(ctx, adapter, store, tree)
	if err != nil {
		return nil, err
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Layout: unixfs.LayoutRootedV1, Remote: store, Blocks: store, Verifier: store.e})
	if err != nil {
		return nil, err
	}
	// Independently read every fixture object through the client before timing.
	for _, name := range names {
		read, err := reader.ReadFile(ctx, result.Key, name)
		if err != nil {
			return nil, fmt.Errorf("source readback failed for %s: %w", name, err)
		}
		if read == nil || !bytes.Equal(read.Body, q.Files[name]) {
			return nil, fmt.Errorf("source readback bytes disagree for %s", name)
		}
	}
	return &session{root: result.Key, store: store, reader: reader}, nil
}

func (s *session) execute(ctx context.Context, q request) response {
	r := response{Schema: responseSchema, ID: q.ID, Root: s.root.String()}
	s.store.proofBytes, s.store.casBytes, s.store.calls = 0, 0, 0
	if q.Root != s.root.String() {
		r.Error = "query changed caller-selected root"
		return r
	}
	parts, err := unixfs.ParseCanonicalStagedPath(q.Path)
	if err != nil || len(parts) == 0 || strings.Join(parts, "/") != q.Path {
		r.Error = "invalid original path"
		return r
	}
	var read *unixfs.ReadResult
	if q.Operation == "read" {
		read, err = s.reader.ReadFile(ctx, s.root, q.Path)
	} else {
		read, err = s.reader.ReadFileRange(ctx, s.root, q.Path, *q.Offset, *q.Length)
	}
	r.ProofBytes, r.CASBytes, r.Calls = s.store.proofBytes, s.store.casBytes, s.store.calls
	if errors.Is(err, unixfs.ErrNotFound) {
		r.Verified, r.Absent = true, true
		return r
	}
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if read == nil {
		r.Error = "missing verified read result"
		return r
	}
	digest := sha256.Sum256(read.Body)
	r.BodySHA256, r.ReturnedBytes, r.Verified = hex.EncodeToString(digest[:]), uint64(len(read.Body)), true
	return r
}

func decode(data []byte) (request, error) {
	var q request
	if err := strictjson.Decode(data, &q); err != nil {
		return q, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return q, err
	}
	keys := []string{"schema", "id", "operation"}
	switch q.Operation {
	case "init":
		keys = append(keys, "backend", "chunk_bytes", "files")
	case "read":
		keys = append(keys, "root", "path")
	case "range":
		keys = append(keys, "root", "path", "offset", "length")
	case "close":
	default:
		return q, fmt.Errorf("unknown operation")
	}
	if q.Schema != requestSchema || q.ID == "" || len(q.ID) > 128 || len(fields) != len(keys) {
		return q, fmt.Errorf("invalid request or ignored operation fields")
	}
	for _, key := range keys {
		if body, exists := fields[key]; !exists || bytes.Equal(body, []byte("null")) {
			return q, fmt.Errorf("explicit nonnull %s required", key)
		}
	}
	if q.Operation == "range" && (q.Length == nil || *q.Length == 0) {
		return q, fmt.Errorf("positive range length required")
	}
	return q, nil
}

func serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxLine)
	encoder := json.NewEncoder(output)
	var state *session
	ids := map[string]bool{}
	closed := false
	for scanner.Scan() {
		q, err := decode(scanner.Bytes())
		if err != nil {
			return err
		}
		if closed || ids[q.ID] || len(ids) >= 1_000_000 {
			return fmt.Errorf("duplicate, excessive or terminal request")
		}
		ids[q.ID] = true
		r := response{Schema: responseSchema, ID: q.ID}
		if q.Operation == "init" {
			if state != nil {
				return fmt.Errorf("fixture already initialized")
			}
			state, err = initialize(ctx, q)
			if err != nil {
				return err
			}
			r.Root, r.Verified = state.root.String(), true
		} else if q.Operation == "close" {
			closed = true
			r.Verified = true
		} else {
			if state == nil {
				return fmt.Errorf("fixture not initialized")
			}
			r = state.execute(ctx, q)
		}
		if err := encoder.Encode(r); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !closed {
		return fmt.Errorf("missing explicit close")
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "local file worker accepts no arguments")
		os.Exit(1)
	}
	if err := serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
