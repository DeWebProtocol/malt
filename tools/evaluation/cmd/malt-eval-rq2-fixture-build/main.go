// Command malt-eval-rq2-fixture-build constructs the exact shared native and
// browser source fixture for paper RQ2. It computes KZG and IPA complete roots
// from declared bytes, uploads every payload, bootstraps each disposable
// Gateway through the secret evaluation capability, re-fetches and verifies
// both authentication graphs, and only then atomically publishes one strict fixture.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/authenticationgraph"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

const (
	requestTimeout = 30 * time.Minute
	maxSourceBytes = 96 << 20
)

type gatewayRegistration struct {
	backend        string
	baseURL        string
	instanceToken  string
	bootstrapToken string
}

type builtBackend struct {
	backend string
	root    cid.Cid
	engine  *engine.Engine
	objects []protocol.AuthenticationCandidate
}

type artifactDescriptor struct {
	SchemaVersion string                   `json:"schema_version"`
	Path          string                   `json:"path"`
	SHA256        string                   `json:"sha256"`
	Bytes         int64                    `json:"bytes"`
	InitialRoots  []rq2fixture.RootBinding `json:"initial_roots"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "malt-eval-rq2-fixture-build:", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("malt-eval-rq2-fixture-build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourcePath := flags.String("source", "", "strict RQ2 source definition JSON")
	outputPath := flags.String("out", "", "new absolute fixture JSON path")
	kzgBase := flags.String("kzg-base-url", "", "empty disposable KZG Gateway origin")
	kzgInstance := flags.String("kzg-gateway-instance-token", "", "KZG Gateway instance identity")
	kzgBootstrap := flags.String("kzg-bootstrap-authorization-token", "", "distinct KZG bootstrap capability")
	ipaBase := flags.String("ipa-base-url", "", "empty disposable IPA Gateway origin")
	ipaInstance := flags.String("ipa-gateway-instance-token", "", "IPA Gateway instance identity")
	ipaBootstrap := flags.String("ipa-bootstrap-authorization-token", "", "distinct IPA bootstrap capability")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *sourcePath == "" || *outputPath == "" {
		return fmt.Errorf("source, output, and both KZG/IPA Gateway registrations are required")
	}
	registrations := []gatewayRegistration{
		{backend: "kzg", baseURL: *kzgBase, instanceToken: *kzgInstance, bootstrapToken: *kzgBootstrap},
		{backend: "ipa", baseURL: *ipaBase, instanceToken: *ipaInstance, bootstrapToken: *ipaBootstrap},
	}
	for index := range registrations {
		registration, err := validateRegistration(registrations[index])
		if err != nil {
			return err
		}
		registrations[index] = registration
	}
	if err := validateIndependentRegistrations(registrations); err != nil {
		return err
	}
	if registrations[0].baseURL == registrations[1].baseURL {
		return fmt.Errorf("KZG and IPA fixture build requires two independently identified disposable Gateways")
	}
	sourceRaw, err := readPinnedSource(*sourcePath)
	if err != nil {
		return err
	}
	source, err := rq2fixture.DecodeSource(sourceRaw)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	kzgBuild, err := buildBackend(ctx, "kzg", source)
	if err != nil {
		return err
	}
	ipaBuild, err := buildBackend(ctx, "ipa", source)
	if err != nil {
		return err
	}
	roots := []rq2fixture.RootBinding{{Backend: "kzg", CID: kzgBuild.root.String()}, {Backend: "ipa", CID: ipaBuild.root.String()}}
	fixture, err := source.Fixture(roots)
	if err != nil {
		return err
	}
	blocks, err := sourceBlocks(source)
	if err != nil {
		return err
	}
	for index, build := range []*builtBackend{kzgBuild, ipaBuild} {
		if err := initializeGateway(ctx, registrations[index], build, fixture, blocks); err != nil {
			return fmt.Errorf("initialize %s Gateway: %w", build.backend, err)
		}
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if _, err := rq2fixture.Decode(raw); err != nil {
		return fmt.Errorf("self-verify final RQ2 fixture: %w", err)
	}
	output, err := filepath.Abs(*outputPath)
	if err != nil {
		return err
	}
	if err := publishAtomicExclusive(output, raw); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(artifactDescriptor{
		SchemaVersion: "malt-rq2-source-fixture-artifact/v1", Path: output,
		SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(raw)), InitialRoots: roots,
	})
}

func validateRegistration(value gatewayRegistration) (gatewayRegistration, error) {
	value.baseURL = strings.TrimRight(strings.TrimSpace(value.baseURL), "/")
	value.instanceToken, value.bootstrapToken = strings.TrimSpace(value.instanceToken), strings.TrimSpace(value.bootstrapToken)
	parsed, err := url.Parse(value.baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopbackHost(parsed.Hostname())) {
		return gatewayRegistration{}, fmt.Errorf("%s Gateway bootstrap origin must be HTTPS or loopback HTTP", value.backend)
	}
	if !canonicalToken(value.instanceToken) || !canonicalToken(value.bootstrapToken) || value.instanceToken == value.bootstrapToken {
		return gatewayRegistration{}, fmt.Errorf("%s Gateway instance/bootstrap tokens must be distinct canonical SHA-256 values", value.backend)
	}
	return value, nil
}

func validateIndependentRegistrations(values []gatewayRegistration) error {
	seen := make(map[string]string, len(values)*2)
	for _, value := range values {
		for _, credential := range []struct {
			role  string
			token string
		}{{role: "instance", token: value.instanceToken}, {role: "bootstrap", token: value.bootstrapToken}} {
			label := value.backend + " " + credential.role
			token := credential.token
			if previous, exists := seen[token]; exists {
				return fmt.Errorf("KZG and IPA fixture build requires four globally distinct Gateway tokens: %s reuses %s token", label, previous)
			}
			seen[token] = label
		}
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func buildBackend(ctx context.Context, backend string, source *rq2fixture.SourceDefinition) (*builtBackend, error) {
	var scheme engine.ProfileVerifier
	var err error
	switch backend {
	case "kzg":
		scheme, err = kzg.NewScheme()
	case "ipa":
		scheme, err = ipa.NewScheme()
	default:
		return nil, fmt.Errorf("unsupported backend %q", backend)
	}
	if err != nil {
		return nil, err
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		return nil, err
	}
	e := engine.New(input.DefaultRegistry(), profiles)
	candidates, err := source.Candidates(ctx, e, backend)
	if err != nil {
		return nil, err
	}
	root, err := cid.Parse(candidates[len(candidates)-1].Root)
	if err != nil {
		return nil, err
	}
	return &builtBackend{backend: backend, root: root, engine: e, objects: candidates}, nil
}

func sourceBlocks(source *rq2fixture.SourceDefinition) ([]transport.Block, error) {
	byCID := make(map[string]transport.Block)
	add := func(data []byte) error {
		key, err := rawCID(data)
		if err != nil {
			return err
		}
		byCID[key.KeyString()] = transport.Block{Codec: cid.Raw, Data: append([]byte(nil), data...)}
		return nil
	}
	for _, file := range source.DirectFiles {
		if err := add(file.Bytes); err != nil {
			return nil, err
		}
	}
	for _, file := range source.ListFiles {
		for _, chunk := range file.Chunks {
			if err := add(chunk.Bytes); err != nil {
				return nil, err
			}
		}
	}
	keys := make([]string, 0, len(byCID))
	for key := range byCID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	blocks := make([]transport.Block, len(keys))
	for index, key := range keys {
		blocks[index] = byCID[key]
	}
	return blocks, nil
}

func initializeGateway(ctx context.Context, registration gatewayRegistration, build *builtBackend, fixture *rq2fixture.Fixture, blocks []transport.Block) error {
	evaluation, err := gatewaytransport.New(gatewaytransport.Options{
		BaseURL: registration.baseURL, InstanceToken: registration.instanceToken, HTTPClient: &http.Client{Timeout: requestTimeout},
	})
	if err != nil {
		return err
	}
	remote, err := transport.New(transport.Options{BaseURL: registration.baseURL, HTTPClient: evaluation.InstanceHTTPClient()})
	if err != nil {
		return err
	}
	health, err := evaluation.Health(ctx)
	if err != nil {
		return err
	}
	if health.Status != "ok" || health.EvaluationInstanceToken != registration.instanceToken || health.BlobBackend != "embedded" || health.ArcTableMode != "versioned" ||
		health.CommitmentBackends != "ipa,kzg" || health.AuthenticationExactAcceptance != "true" || health.EvaluationAuthenticationBootstrap != gatewaytransport.BootstrapProfile {
		return fmt.Errorf("Gateway health does not expose the exact clean RQ2 bootstrap boundary")
	}
	for offset := 0; offset < len(blocks); offset += transport.MaxCASBatchBlocks {
		part := blocks[offset:min(len(blocks), offset+transport.MaxCASBatchBlocks)]
		results, err := remote.PutBatch(ctx, part)
		if err != nil {
			return fmt.Errorf("upload RQ2 source blocks: %w", err)
		}
		if len(results) != len(part) {
			return fmt.Errorf("source upload result count differs")
		}
		for i, block := range part {
			expected, err := rawCID(block.Data)
			if err != nil || !results[i].CID.Equals(expected) {
				return fmt.Errorf("source upload returned a mismatched CID")
			}
		}
	}
	for i, candidate := range build.objects {
		result, err := evaluation.BootstrapEvaluationObject(ctx, registration.bootstrapToken, gatewaytransport.BootstrapObject{OperationID: fmt.Sprintf("rq2-%s-bootstrap-%03d", build.backend, i), Candidate: candidate})
		if err != nil {
			return fmt.Errorf("bootstrap candidate %d: %w", i, err)
		}
		if result.Root.String() != candidate.Root {
			return fmt.Errorf("bootstrap candidate %d returned an unexpected Root", i)
		}
	}
	session, err := authenticationgraph.New(evaluation, build.engine)
	if err != nil {
		return err
	}
	if _, err := session.Load(ctx, build.root); err != nil {
		return fmt.Errorf("import bootstrapped authentication graph: %w", err)
	}
	view, err := session.Snapshot()
	if err != nil {
		return err
	}
	if err := fixture.ValidateInitialGraph(view, build.backend); err != nil {
		return fmt.Errorf("bootstrapped source/root oracle: %w", err)
	}

	return nil
}

func rawCID(data []byte) (cid.Cid, error) {
	digest, err := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: 32}.Sum(data)
	return digest, err
}

func readPinnedSource(path string) ([]byte, error) {
	lstat, err := os.Lstat(path)
	if err != nil || lstat.Mode()&os.ModeSymlink != 0 || !lstat.Mode().IsRegular() || lstat.Size() <= 0 || lstat.Size() > maxSourceBytes {
		return nil, fmt.Errorf("RQ2 source definition is not a bounded regular file: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(lstat, opened) {
		return nil, fmt.Errorf("RQ2 source definition changed before it was opened")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil {
		return nil, err
	}
	post, err := os.Lstat(path)
	if err != nil || !os.SameFile(lstat, post) || int64(len(raw)) != lstat.Size() {
		return nil, fmt.Errorf("RQ2 source definition changed while it was read")
	}
	return raw, nil
}

func publishAtomicExclusive(path string, raw []byte) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("fixture output path must be absolute")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return fmt.Errorf("fixture output already exists")
		}
		return err
	}
	temporary, err := os.CreateTemp(directory, ".rq2-fixture-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	succeeded := false
	defer func() {
		_ = temporary.Close()
		if !succeeded {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(path)
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	if syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(syncErr, closeErr)
	}
	succeeded = true
	return nil
}

func canonicalToken(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
