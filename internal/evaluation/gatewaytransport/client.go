// Package gatewaytransport owns evaluator-only Gateway HTTP capabilities.
// Its instance client injects the disposable evaluation identity into every
// request, while bootstrap authorization uses a separate client so the
// controller-only secret is never combined with the instance token.
package gatewaytransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/merkledag"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const (
	InstanceTokenHeader               = "X-Malt-Evaluation-Instance-Token"
	BootstrapAuthorizationTokenHeader = "X-Malt-Evaluation-Bootstrap-Authorization"
	BootstrapProfile                  = "gateway.evaluation-client-root-bootstrap-object/v1"

	defaultMaxJSONResponseBytes  int64 = 96 << 20
	defaultMaxBlobResponseBytes  int64 = 64 << 20
	defaultMaxErrorResponseBytes int64 = 1 << 20

	clientRootWriteAccountingProfile = "gateway.client-root-write-accounting/v1"
	clientRootWriteByteMethod        = "logical-kv-key-plus-value-bytes/v1"
)

var clientRootWriteCategories = []string{"semantic-materialization", "arctable-records", "root-version-metadata"}

type Options struct {
	BaseURL               string
	InstanceToken         string
	HTTPClient            *http.Client
	MaxJSONResponseBytes  int64
	MaxBlobResponseBytes  int64
	MaxErrorResponseBytes int64
}

// Client is an evaluator-only control and data-plane transport. Product code
// must use package transport instead.
type Client struct {
	baseURL               *url.URL
	plainHTTP             *http.Client
	instanceHTTP          *http.Client
	maxJSONResponseBytes  int64
	maxBlobResponseBytes  int64
	maxErrorResponseBytes int64
}

// Health is the strict evaluation projection of Gateway health. The public
// transport health value deliberately omits these evaluator-only capabilities.
type Health struct {
	Status                        string `json:"status"`
	EvaluationInstanceToken       string `json:"evaluation_instance_token,omitempty"`
	KVBackend                     string `json:"kv_backend,omitempty"`
	BlobBackend                   string `json:"blob_backend,omitempty"`
	ArcTableMode                  string `json:"arctable_mode,omitempty"`
	CommitmentProfile             string `json:"default_commitment_backend,omitempty"`
	CommitmentBackends            string `json:"commitment_backends,omitempty"`
	EvaluationCASWriteAccounting  string `json:"evaluation_cas_write_accounting,omitempty"`
	EvaluationCASWriteIsolation   string `json:"evaluation_cas_write_isolation,omitempty"`
	ClientRootWriteAccounting     string `json:"client_root_write_accounting,omitempty"`
	ClientRootExactAcceptance     string `json:"client_root_exact_acceptance,omitempty"`
	EvaluationClientRootBootstrap string `json:"evaluation_client_root_bootstrap,omitempty"`
}

type BootstrapEntry struct {
	Path   *string
	Index  *uint64
	Target cid.Cid
}

type BootstrapObject struct {
	OperationID  string
	Kind         arcset.Kind
	Backend      maltcid.BackendKind
	ExpectedRoot cid.Cid
	Entries      []BootstrapEntry
	Commit       mutation.CommitDescriptor
}

type BootstrapResult struct {
	Root            cid.Cid
	ReplayNanos     uint64
	PersistNanos    uint64
	WriteAccounting transport.ClientRootWriteAccounting
}

// RawCASGetter adapts the evaluator-only raw endpoint to merkledag.BlockGetter.
// The Merkle DAG verifier, not this transport, binds returned bytes to the CID.
type RawCASGetter struct {
	Client *Client
}

func (g RawCASGetter) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if g.Client == nil {
		return nil, fmt.Errorf("evaluation raw CAS client is nil")
	}
	return g.Client.GetRawCAS(ctx, key)
}

func New(options Options) (*Client, error) {
	baseURL, err := parseBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}
	if !canonicalLowerSHA256(options.InstanceToken) {
		return nil, fmt.Errorf("evaluation instance token must be a canonical SHA-256")
	}
	if baseURL.Scheme != "https" && !(baseURL.Scheme == "http" && isLoopbackHost(baseURL.Hostname())) {
		return nil, fmt.Errorf("evaluation instance token requires HTTPS or a loopback HTTP Gateway origin")
	}
	maxJSON, err := responseLimit(options.MaxJSONResponseBytes, defaultMaxJSONResponseBytes, "JSON")
	if err != nil {
		return nil, err
	}
	maxBlob, err := responseLimit(options.MaxBlobResponseBytes, defaultMaxBlobResponseBytes, "blob")
	if err != nil {
		return nil, err
	}
	maxError, err := responseLimit(options.MaxErrorResponseBytes, defaultMaxErrorResponseBytes, "error")
	if err != nil {
		return nil, err
	}
	baseHTTP := options.HTTPClient
	if baseHTTP == nil {
		baseHTTP = &http.Client{Timeout: 5 * time.Minute}
	}
	plainHTTP := cloneWithoutRedirects(baseHTTP, "evaluation Gateway")
	instanceHTTP := cloneWithoutRedirects(baseHTTP, "evaluation instance")
	roundTripper := instanceHTTP.Transport
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}
	instanceHTTP.Transport = instanceTokenTransport{base: roundTripper, token: options.InstanceToken, allowedBase: baseURL}
	return &Client{
		baseURL: baseURL, plainHTTP: plainHTTP, instanceHTTP: instanceHTTP,
		maxJSONResponseBytes: maxJSON, maxBlobResponseBytes: maxBlob, maxErrorResponseBytes: maxError,
	}, nil
}

// InstanceHTTPClient returns a copy of the no-redirect HTTP client that injects
// the evaluation instance token. Evaluation adapters pass it to the ordinary
// untrusted transport so update-view, client-root, and CAS requests are bound
// to the same disposable Gateway instance.
func (c *Client) InstanceHTTPClient() *http.Client {
	if c == nil || c.instanceHTTP == nil {
		return nil
	}
	copyClient := *c.instanceHTTP
	return &copyClient
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/healthz"), nil)
	if err != nil {
		return nil, err
	}
	var health Health
	if err := c.executeJSON(c.instanceHTTP, request, &health); err != nil {
		return nil, err
	}
	return &health, nil
}

// BootstrapEvaluationObject installs one first-campaign semantic object with
// the controller-only bootstrap secret. It intentionally uses plainHTTP, so
// the instance token is not attached to this distinct authorization boundary.
func (c *Client) BootstrapEvaluationObject(ctx context.Context, bootstrapAuthorizationToken string, value BootstrapObject) (BootstrapResult, error) {
	if !canonicalLowerSHA256(bootstrapAuthorizationToken) {
		return BootstrapResult{}, fmt.Errorf("evaluation bootstrap authorization token must be a canonical SHA-256")
	}
	emptyMeasuredList := value.Kind == arcset.KindList && len(value.Entries) == 0 && value.Commit.FixedList != nil &&
		value.Commit.FixedList.TotalSize == 0 && value.Commit.FixedList.ChunkSize > 0
	if value.OperationID == "" || (value.Kind != arcset.KindMap && value.Kind != arcset.KindList) ||
		(value.Backend != maltcid.BackendKindKZG && value.Backend != maltcid.BackendKindIPA) ||
		!value.ExpectedRoot.Defined() || len(value.Entries) == 0 && !emptyMeasuredList {
		return BootstrapResult{}, fmt.Errorf("evaluation bootstrap object is incomplete")
	}
	type wireEntry struct {
		Path   *string `json:"path,omitempty"`
		Index  *uint64 `json:"index,omitempty"`
		Target string  `json:"target"`
	}
	type fixedList struct {
		TotalSize uint64 `json:"total_size"`
		ChunkSize uint64 `json:"chunk_size"`
	}
	requestBody := struct {
		Profile      string      `json:"profile"`
		OperationID  string      `json:"operation_id"`
		Kind         string      `json:"kind"`
		Backend      string      `json:"backend"`
		ExpectedRoot string      `json:"expected_root"`
		Entries      []wireEntry `json:"entries"`
		FixedList    *fixedList  `json:"fixed_list,omitempty"`
	}{
		Profile: BootstrapProfile, OperationID: value.OperationID, Kind: string(value.Kind),
		Backend: string(value.Backend), ExpectedRoot: value.ExpectedRoot.String(), Entries: make([]wireEntry, len(value.Entries)),
	}
	for index, entry := range value.Entries {
		if !entry.Target.Defined() || (value.Kind == arcset.KindMap && (entry.Path == nil || entry.Index != nil)) ||
			(value.Kind == arcset.KindList && (entry.Path != nil || entry.Index == nil)) {
			return BootstrapResult{}, fmt.Errorf("evaluation bootstrap entry %d is invalid", index)
		}
		requestBody.Entries[index] = wireEntry{Path: entry.Path, Index: entry.Index, Target: entry.Target.String()}
	}
	if value.Commit.FixedList != nil {
		requestBody.FixedList = &fixedList{TotalSize: value.Commit.FixedList.TotalSize, ChunkSize: value.Commit.FixedList.ChunkSize}
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return BootstrapResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/evaluation/client-root/bootstrap-object"), bytes.NewReader(encoded))
	if err != nil {
		return BootstrapResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(BootstrapAuthorizationTokenHeader, bootstrapAuthorizationToken)
	response, err := c.plainHTTP.Do(request)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return BootstrapResult{}, c.responseError(response)
	}
	if err := requireJSONNoStore(response); err != nil {
		return BootstrapResult{}, err
	}
	raw, err := readBounded(response.Body, c.maxJSONResponseBytes, "Gateway evaluation bootstrap response")
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return BootstrapResult{}, fmt.Errorf("Gateway evaluation bootstrap response: %w", err)
	}
	var wire struct {
		Profile         string                              `json:"profile"`
		Root            string                              `json:"root"`
		ReplayNanos     uint64                              `json:"replay_nanos"`
		PersistNanos    uint64                              `json:"persist_nanos"`
		WriteAccounting transport.ClientRootWriteAccounting `json:"write_accounting"`
	}
	if err := decodeStrict(raw, &wire); err != nil {
		return BootstrapResult{}, fmt.Errorf("decode Gateway evaluation bootstrap response: %w", err)
	}
	root, err := cid.Parse(wire.Root)
	if err != nil || !root.Equals(value.ExpectedRoot) || wire.Profile != BootstrapProfile || maltcid.BackendKindOf(root) != value.Backend ||
		(value.Kind == arcset.KindMap && maltcid.SemanticKindOf(root) != maltcid.SemanticKindMap) ||
		(value.Kind == arcset.KindList && maltcid.SemanticKindOf(root) != maltcid.SemanticKindList) {
		return BootstrapResult{}, fmt.Errorf("Gateway evaluation bootstrap returned a mismatched semantic root")
	}
	if err := validateWriteAccounting(wire.WriteAccounting); err != nil || !wire.WriteAccounting.Available ||
		strings.TrimSpace(wire.WriteAccounting.UnavailableReason) != "" {
		return BootstrapResult{}, fmt.Errorf("Gateway evaluation bootstrap returned invalid exact write accounting")
	}
	return BootstrapResult{
		Root: root, ReplayNanos: wire.ReplayNanos, PersistNanos: wire.PersistNanos, WriteAccounting: wire.WriteAccounting,
	}, nil
}

// GetRawCAS returns bounded bytes without hashing them. Only the Direct-CAS
// evaluator uses this method so its local verifier can own and time the single
// CID hash.
func (c *Client) GetRawCAS(ctx context.Context, key cid.Cid) ([]byte, error) {
	if !key.Defined() {
		return nil, fmt.Errorf("Gateway CAS key is undefined")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/v1/cas/"+url.PathEscape(key.String())), nil)
	if err != nil {
		return nil, err
	}
	response, err := c.instanceHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		responseErr := c.responseError(response)
		if response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", clientcas.ErrNotFound, responseErr)
		}
		return nil, responseErr
	}
	return readBounded(response.Body, c.maxBlobResponseBytes, "Gateway evaluation CAS body")
}

// ReadCAR invokes the evaluator-only selective-CAR route. Callers must pass the
// returned bytes to merkledag.VerifyMerkleDAGCARRead before using any result.
func (c *Client) ReadCAR(ctx context.Context, encodedRequest []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/compat/merkledag/car/read"), bytes.NewReader(encodedRequest))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.instanceHTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, c.responseError(response)
	}
	if got := response.Header.Get("Content-Type"); got != merkledag.MerkleDAGCARReadMediaType {
		return nil, fmt.Errorf("unsupported Gateway Merkle-DAG CAR content type %q", got)
	}
	return readBounded(response.Body, c.maxBlobResponseBytes, "Gateway Merkle-DAG CAR response")
}

type instanceTokenTransport struct {
	base        http.RoundTripper
	token       string
	allowedBase *url.URL
}

func (t instanceTokenTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !withinBaseURL(request.URL, t.allowedBase) ||
		request.Host != "" && !strings.EqualFold(request.Host, t.allowedBase.Host) {
		return nil, fmt.Errorf("refusing to send evaluation instance token outside configured Gateway base URL")
	}
	copyRequest := request.Clone(request.Context())
	copyRequest.Header = request.Header.Clone()
	copyRequest.Header.Set(InstanceTokenHeader, t.token)
	return t.base.RoundTrip(copyRequest)
}

func cloneWithoutRedirects(source *http.Client, label string) *http.Client {
	copyClient := *source
	copyClient.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		return fmt.Errorf("refusing %s redirect to %s", label, next.URL.Redacted())
	}
	return &copyClient
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Opaque != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "" && (parsed.Path[0] != '/' || path.Clean(parsed.Path) != parsed.Path) {
		return nil, fmt.Errorf("evaluation Gateway base URL must be an absolute HTTP(S) URL without query or fragment")
	}
	return parsed, nil
}

func withinBaseURL(candidate, base *url.URL) bool {
	if candidate == nil || base == nil || !strings.EqualFold(candidate.Scheme, base.Scheme) ||
		!strings.EqualFold(candidate.Host, base.Host) || candidate.User != nil ||
		candidate.Opaque != "" || candidate.RawPath != "" ||
		candidate.Path != "" && (candidate.Path[0] != '/' || path.Clean(candidate.Path) != candidate.Path) {
		return false
	}
	basePath := strings.TrimSuffix(base.Path, "/")
	return basePath == "" || candidate.Path == basePath || strings.HasPrefix(candidate.Path, basePath+"/")
}

func (c *Client) endpoint(route string) string {
	copyURL := *c.baseURL
	copyURL.Path = path.Join(copyURL.Path, route)
	if strings.HasSuffix(route, "/") && !strings.HasSuffix(copyURL.Path, "/") {
		copyURL.Path += "/"
	}
	return copyURL.String()
}

func (c *Client) executeJSON(client *http.Client, request *http.Request, output any) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return c.responseError(response)
	}
	data, err := readBounded(response.Body, c.maxJSONResponseBytes, "Gateway evaluation JSON response")
	if err != nil {
		return err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return fmt.Errorf("decode Gateway evaluation JSON response: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode Gateway evaluation JSON response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode Gateway evaluation JSON response: trailing JSON")
		}
		return fmt.Errorf("decode Gateway evaluation JSON response: %w", err)
	}
	return nil
}

func (c *Client) responseError(response *http.Response) error {
	data, err := readBounded(response.Body, c.maxErrorResponseBytes, "Gateway evaluation error response")
	if err != nil {
		return err
	}
	var body struct {
		Error   string `json:"error,omitempty"`
		Message string `json:"message,omitempty"`
	}
	message := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &body) == nil {
		if body.Message != "" {
			message = body.Message
		} else if body.Error != "" {
			message = body.Error
		}
	}
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return &transport.Error{StatusCode: response.StatusCode, Message: message}
}

func requireJSONNoStore(response *http.Response) error {
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType != "application/json" {
		return fmt.Errorf("Gateway evaluation bootstrap response has unsupported Content-Type %q", response.Header.Get("Content-Type"))
	}
	for _, directive := range strings.Split(strings.ToLower(response.Header.Get("Cache-Control")), ",") {
		if strings.TrimSpace(directive) == "no-store" {
			return nil
		}
	}
	return fmt.Errorf("Gateway evaluation bootstrap response is missing Cache-Control: no-store")
}

func validateWriteAccounting(accounting transport.ClientRootWriteAccounting) error {
	if accounting.Profile != clientRootWriteAccountingProfile || accounting.ByteMethod != clientRootWriteByteMethod {
		return fmt.Errorf("Gateway client-root write accounting has unsupported profile/method")
	}
	if !accounting.Available {
		if accounting.UnavailableReason == "" || accounting.ObjectLedgerSHA256 != "" || len(accounting.Categories) != 0 {
			return fmt.Errorf("unavailable Gateway client-root write accounting carries measurements")
		}
		return nil
	}
	if accounting.UnavailableReason != "" || !canonicalLowerSHA256(accounting.ObjectLedgerSHA256) ||
		len(accounting.Categories) != len(clientRootWriteCategories) {
		return fmt.Errorf("available Gateway client-root write accounting is incomplete")
	}
	for index, category := range accounting.Categories {
		attempts := category.AttemptedNewWrites + category.AttemptedReplacementWrites + category.AttemptedSameValueWrites + category.AttemptedDeleteWrites
		attemptBytes := category.AttemptedNewBytes + category.AttemptedReplacementBytes + category.AttemptedSameValueBytes + category.AttemptedDeleteBytes
		if category.Category != clientRootWriteCategories[index] || category.AttemptedWrites != attempts ||
			category.AttemptedBytes != attemptBytes || category.NetBytes != int64(category.GrossNewBytes)-int64(category.ReclaimedBytes) ||
			category.NewlyPersistedWrites != category.NewWrites+category.ReplacedWrites ||
			category.GrossNewBytes != category.NewBytes+category.ReplacementNewBytes ||
			category.ReclaimedBytes != category.ReplacementReclaimedBytes+category.DeletedReclaimedBytes {
			return fmt.Errorf("Gateway client-root write accounting category %d is inconsistent", index)
		}
	}
	return nil
}

func canonicalLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func responseLimit(value, fallback int64, kind string) (int64, error) {
	if value < 0 {
		return 0, fmt.Errorf("maximum %s response size must not be negative", kind)
	}
	if value == 0 {
		return fallback, nil
	}
	return value, nil
}

func readBounded(reader io.Reader, limit int64, description string) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%s limit is invalid", description)
	}
	readLimit := limit
	if limit < int64(^uint64(0)>>1) {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(reader, readLimit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d-byte limit", description, limit)
	}
	return data, nil
}

func decodeStrict(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing token %v", token)
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			rawKey, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := rawKey.(string)
			if !ok {
				return fmt.Errorf("%s has a non-string object key", location)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%s has duplicate key %q", location, key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, location+"."+key); err != nil {
				return err
			}
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := scanJSONValue(decoder, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s has unexpected delimiter %q", location, delimiter)
	}
	_, err = decoder.Token()
	return err
}
