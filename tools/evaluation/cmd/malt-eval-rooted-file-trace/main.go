// malt-eval-rooted-file-trace compiles bounded file snapshots through the real
// rooted-v1 application schema. It exports authentication-only evaluation data.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type version struct {
	Files   map[string][]byte `json:"files"`
	Queries []string          `json:"queries"`
}
type trace struct {
	Schema       string                             `json:"schema"`
	Integration  string                             `json:"integration"`
	SourceSHA256 string                             `json:"source_sha256"`
	Candidates   []protocol.AuthenticationCandidate `json:"candidates"`
	Queries      []protocol.AuthenticationRequest   `json:"queries"`
}
type sink struct {
	e          *engine.Engine
	candidates map[string]protocol.AuthenticationCandidate
	ordered    []protocol.AuthenticationCandidate
}

func (s *sink) MaterializeAuthentication(ctx context.Context, c protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := authentication.ValidateCandidate(ctx, s.e, c); err != nil {
		return cid.Undef, err
	}
	root, err := cid.Decode(c.Root)
	if err != nil {
		return cid.Undef, err
	}
	if _, ok := s.candidates[c.Root]; !ok {
		s.ordered = append(s.ordered, c)
		s.candidates[c.Root] = c
	}
	return root, nil
}
func (s *sink) AuthenticationCandidate(_ context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	c, ok := s.candidates[root.String()]
	if !ok {
		return nil, fmt.Errorf("base candidate missing")
	}
	return &c, nil
}
func (s *sink) Put(ctx context.Context, b []byte) (cid.Cid, error) {
	return s.PutWithCodec(ctx, b, cid.Raw)
}
func (*sink) PutWithCodec(_ context.Context, b []byte, codec uint64) (cid.Cid, error) {
	return (cid.Prefix{Version: 1, Codec: codec, MhType: mh.SHA2_256, MhLength: -1}).Sum(b)
}
func compile(ctx context.Context, data []byte, backend string, chunk int) (trace, error) {
	out := trace{Schema: "malt.rooted-trace/v1", Integration: "malt.unixfs/rooted-v1/authentication-only"}
	if chunk <= 0 || chunk > 64<<20 {
		return out, fmt.Errorf("chunk size outside 1..64MiB")
	}
	var versions []version
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&versions); err != nil {
		return out, err
	}
	if d.Decode(new(any)) != io.EOF {
		return out, fmt.Errorf("trailing data")
	}
	if len(versions) == 0 || len(versions) > 1000 {
		return out, fmt.Errorf("snapshot count outside 1..1000")
	}
	digest := sha256.Sum256(data)
	out.SourceSHA256 = hex.EncodeToString(digest[:])
	profiles := engine.NewRegistry()
	profile := maltcid.KZG4096
	switch backend {
	case "kzg":
		s, err := kzg.NewScheme()
		if err != nil {
			return out, err
		}
		if err = profiles.Register(s); err != nil {
			return out, err
		}
	case "ipa":
		profile = maltcid.IPA256
		s, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
		if err != nil {
			return out, err
		}
		if err = profiles.Register(s); err != nil {
			return out, err
		}
	default:
		return out, fmt.Errorf("unknown backend")
	}
	store := &sink{e: engine.New(profiles), candidates: map[string]protocol.AuthenticationCandidate{}}
	adapter, err := unixfs.NewAuthenticationAdapter(unixfs.LayoutRootedV1, store, store.e, profile)
	if err != nil {
		return out, err
	}
	layout, _ := unixfs.NewLayout(unixfs.LayoutRootedV1)
	previous := map[string]cid.Cid{}
	for _, v := range versions {
		tree := unixfs.NewStagedDirectory()
		tree.Changed = true
		names := make([]string, 0, len(v.Files))
		for name := range v.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			parts, err := unixfs.ParseCanonicalStagedPath(name)
			if err != nil || len(parts) == 0 {
				return out, fmt.Errorf("invalid file path %q", name)
			}
			for j := 1; j < len(parts); j++ {
				if _, ok := v.Files[strings.Join(parts[:j], "/")]; ok {
					return out, fmt.Errorf("file/directory collision: %s", name)
				}
			}
			body := v.Files[name]
			target, _, err := unixfs.MaterializeStagedFilePayload(ctx, store, adapter, bytes.NewReader(body), int64(len(body)), chunk)
			if err != nil {
				return out, err
			}
			if err = unixfs.SetStagedFile(tree, name, target); err != nil {
				return out, err
			}
		}
		var bind func(*unixfs.StagedNode, string)
		bind = func(n *unixfs.StagedNode, path string) {
			if n.Kind != unixfs.StagedKindDirectory {
				return
			}
			n.Key = previous[path]
			n.Changed = true
			for name, child := range n.Children {
				p := name
				if path != "" {
					p = path + "/" + name
				}
				bind(child, p)
			}
		}
		bind(tree, "")
		result, err := layout.Materialize(ctx, adapter, store, tree)
		if err != nil {
			return out, err
		}
		previous = map[string]cid.Cid{}
		var remember func(*unixfs.StagedNode, string)
		remember = func(n *unixfs.StagedNode, path string) {
			if n.Kind != unixfs.StagedKindDirectory {
				return
			}
			previous[path] = n.Key
			for name, child := range n.Children {
				p := name
				if path != "" {
					p = path + "/" + name
				}
				remember(child, p)
			}
		}
		remember(tree, "")
		for _, path := range v.Queries {
			parts, err := unixfs.ParseCanonicalStagedPath(path)
			if err != nil {
				return out, err
			}
			steps := make([][]byte, 0, len(parts))
			for _, part := range parts {
				steps = append(steps, []byte(part))
			}
			out.Queries = append(out.Queries, protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: result.Key.String(), Steps: steps, Operation: "resolve"})
		}
	}
	out.Candidates = store.ordered
	if len(out.Queries) == 0 {
		return out, fmt.Errorf("queries are required")
	}
	return out, nil
}
func run() error {
	file := flag.String("input", "", "JSON array of complete file snapshots; file values are base64")
	backend := flag.String("backend", "kzg", "kzg or ipa")
	chunk := flag.Int("chunk-size", 256<<10, "file chunk bytes")
	flag.Parse()
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 256<<20 {
		return fmt.Errorf("input exceeds256MiB")
	}
	out, err := compile(context.Background(), data, *backend, *chunk)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
