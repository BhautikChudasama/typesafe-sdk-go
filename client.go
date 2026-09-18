package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Defaults for the settings [ClientOptions] leaves unset and the environment
// does not supply.
const (
	// DefaultBaseURL is the production API root.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel is the model used by a request that names none. The
	// "-latest" suffix tracks the newest release of that model family.
	DefaultModel = "jev-latest"
)

// ClientOptions configures a [Client]. The zero value is usable as long as
// [EnvAPIKey] is set in the environment.
//
// Every string setting falls back to its environment variable and then to the
// SDK default, in that order.
type ClientOptions struct {
	// APIKey authenticates every request. It falls back to [EnvAPIKey], and
	// [NewClient] fails with [ErrNoAPIKey] when neither supplies one.
	APIKey string
	// BaseURL is the API root, without a trailing slash. It falls back to
	// [EnvBaseURL] and then [DefaultBaseURL].
	BaseURL string
	// DefaultModel answers requests that leave [SystemOneRequest.Model] empty.
	// It falls back to [EnvDefaultModel] and then [DefaultModel].
	DefaultModel string
	// Timeout bounds each attempt, not the call as a whole: a retried call may
	// take several times as long. Bound the whole call with its
	// [context.Context]. Zero means [DefaultTimeout].
	Timeout time.Duration
	// Retry replaces the SDK's retry policy. Nil means [DefaultRetryPolicy].
	Retry *RetryPolicy
	// Header is sent with every request. Per-call headers in
	// [RequestOptions.Header] override it, and neither can displace the headers
	// this package sets to authenticate and identify the request.
	Header http.Header
	// HTTPClient sends the requests. Nil means [http.DefaultClient].
	//
	// Any timeout on it applies to the whole attempt including the body, as
	// Timeout already does. Set its Transport to route through a proxy, pin
	// TLS, or observe traffic.
	HTTPClient *http.Client
	// Logger records request and response activity: summaries at info, and
	// full headers and bodies at debug. Credential headers are redacted;
	// bodies are not, and yours may hold the data you are evaluating.
	//
	// Nil means no logging, unless [EnvLogLevel] is set, which installs a
	// stderr logger at that level.
	Logger *slog.Logger
}

// A Client is a connection-pooled handle for the TypeSafe API. It is safe for
// concurrent use, and one client should be shared for the life of a program so
// that its underlying connections are reused.
type Client struct {
	apiKey       string
	baseURL      string
	defaultModel string
	timeout      time.Duration
	retry        RetryPolicy
	header       http.Header
	httpClient   *http.Client
	logger       *slog.Logger

	// requests numbers calls for [call.tag].
	requests atomic.Int64
}

// NewClient returns a client for the TypeSafe API.
//
// Options may be nil, which configures the client entirely from the
// environment. It returns an error when no API key is available, or when a
// setting is one this package refuses.
func NewClient(options *ClientOptions) (*Client, error) {
	var opts ClientOptions
	if options != nil {
		opts = *options
	}

	apiKey := fromCodeOrEnv(opts.APIKey, EnvAPIKey, "")
	if apiKey == "" {
		return nil, fmt.Errorf("%w: pass ClientOptions.APIKey or set %s", ErrNoAPIKey, EnvAPIKey)
	}

	baseURL := strings.TrimRight(fromCodeOrEnv(opts.BaseURL, EnvBaseURL, DefaultBaseURL), "/")
	if parsed, err := url.Parse(baseURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errorf("BaseURL %q is not an absolute URL", baseURL)
	}

	if opts.Timeout < 0 {
		return nil, errorf("Timeout must not be negative, got %s", opts.Timeout)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	retry := DefaultRetryPolicy()
	if opts.Retry != nil {
		if err := opts.Retry.validate("Retry"); err != nil {
			return nil, err
		}
		retry = opts.Retry.clone()
	}

	logger, err := resolveLogger(opts.Logger)
	if err != nil {
		return nil, err
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		apiKey:       apiKey,
		baseURL:      baseURL,
		defaultModel: fromCodeOrEnv(opts.DefaultModel, EnvDefaultModel, DefaultModel),
		timeout:      timeout,
		retry:        retry,
		header:       opts.Header.Clone(),
		httpClient:   httpClient,
		logger:       logger,
	}, nil
}

// BaseURL reports the API root the client sends to, without a trailing slash.
func (c *Client) BaseURL() string { return c.baseURL }

// DefaultModel reports the model used by requests that name none.
func (c *Client) DefaultModel() string { return c.defaultModel }

// Timeout reports the per-attempt timeout.
func (c *Client) Timeout() time.Duration { return c.timeout }

// Retry reports a copy of the client's retry policy. Changing it does not
// affect the client; pass a policy to [NewClient] or [RequestOptions] instead.
func (c *Client) Retry() RetryPolicy { return c.retry.clone() }

// RequestOptions overrides client settings for one call. A nil *RequestOptions,
// and every zero field of a non-nil one, inherits the client's setting.
type RequestOptions struct {
	// Timeout replaces [ClientOptions.Timeout] for each attempt of this call.
	Timeout time.Duration
	// Retry replaces the client's retry policy for this call, in full: it is a
	// complete policy and not a patch. See [RetryPolicy].
	Retry *RetryPolicy
	// Header is merged over [ClientOptions.Header] for this call.
	Header http.Header
}

// Meta reports HTTP metadata about a response from the API. It reaches a caller
// on a result; an unsuccessful response carries the same facts on an
// [*APIError].
type Meta struct {
	// RequestID is the x-typesafe-request-id header, or "" when absent. Quote
	// it in bug reports: it identifies the request in the service's own logs.
	RequestID string
	// Status is the HTTP response status code, which for a result is 2xx.
	Status int
	// Header holds the HTTP response headers.
	Header http.Header
	// Body is the raw response body. The typed fields of a result drop any
	// property this SDK does not model, and this is where to find one: a model
	// attribute added after this release, for instance.
	Body []byte
}

// Headers this package reads or writes, spelled in [http.Header] canonical
// form. Writing them any other way still reaches the wire canonicalized, but
// leaves the literal here disagreeing with the one a reader greps for.
const (
	requestIDHeader  = "X-Typesafe-Request-Id"
	retryCountHeader = "X-Typesafe-Retry-Count"
	sdkHeader        = "X-Typesafe-Sdk"
	runtimeHeader    = "X-Typesafe-Runtime"
)

// describe names a response in an error about its content. An answer this
// package cannot read is the one case where a caller has nothing to report but
// the identifier, so every such error carries it.
func (m Meta) describe() string {
	if m.RequestID == "" {
		return "the response"
	}
	return "response " + m.RequestID
}

// apiError reports a non-2xx response as the error it is. It lives here rather
// than at the call site so that a response and the error made from it cannot
// drift apart.
func (m Meta) apiError() *APIError {
	return &APIError{
		Status:    m.Status,
		Header:    m.Header,
		Body:      parseBody(m.Body),
		RequestID: m.RequestID,
	}
}

// A SystemOneRequest asks named questions about a piece of state.
type SystemOneRequest struct {
	// State is what the questions are asked about, and is required: text, a
	// JSON object, or a JSON array. The service refuses a null state, and
	// refuses one that encodes as a number or a boolean.
	//
	// An empty string, object, or array is a state; nil is not.
	State Entry `json:"state"`
	// Questions are the questions to answer, keyed by the names their answers
	// come back under. At least one is required.
	Questions Questions `json:"questions"`
	// Model overrides the client's default model for this request.
	Model string `json:"model"`
	// Extra carries additional top-level request properties, for using a
	// service feature this SDK does not model yet. Its keys are merged into the
	// request body and may not collide with the fields above.
	Extra map[string]any `json:"-"`
}

// Validate reports whether the request can be sent.
//
// [Client.SystemOne] calls it before the first attempt, so that a malformed
// request costs no network attempt and the error names what is wrong with it.
// Call it yourself when a request is assembled far from where it is sent.
func (r *SystemOneRequest) Validate() error {
	if r == nil {
		return errorf("a request is required")
	}
	if r.State == nil {
		return errorf("State is required; the service refuses a null state")
	}
	return r.Questions.Validate()
}

// MarshalJSON encodes the request with [SystemOneRequest.Extra] merged in.
//
// The fields are encoded and decoded again rather than being listed here, so
// that the struct tags stay the only statement of what this request sends. A
// second list would be a second owner of that fact, and the collision check
// below would start passing keys that do collide.
func (r *SystemOneRequest) MarshalJSON() ([]byte, error) {
	type wire SystemOneRequest
	encoded, err := json.Marshal((*wire)(r))
	if err != nil || len(r.Extra) == 0 {
		return encoded, err
	}
	return mergeExtra(encoded, r.Extra)
}

func mergeExtra(encoded []byte, extra map[string]any) ([]byte, error) {
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for key, value := range extra {
		if _, taken := merged[key]; taken {
			return nil, errorf("Extra key %q collides with a request field", key)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, errorf("encoding Extra key %q: %w", key, err)
		}
		merged[key] = raw
	}
	return json.Marshal(merged)
}

// A SystemOneResult holds the answers to a [SystemOneRequest].
type SystemOneResult struct {
	// Model is the model that answered, resolved from an alias such as
	// "jev-latest" to the release it names.
	Model string `json:"model"`
	// Answers holds one answer per question, under the same names.
	Answers Answers `json:"answers"`
	// Usage reports the tokens the request consumed.
	Usage Usage `json:"usage"`
	// Meta reports HTTP metadata about the response.
	Meta Meta `json:"-"`
}

// Usage reports the tokens a request consumed.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// SystemOne answers named questions about text or structured state.
//
// Each question is evaluated in parallel and in isolation against the same
// state, and comes back as a probability distribution rather than as prose to
// parse. Retrieve the answers with [Answers.Noul], [Answers.Choice], and
// [Answers.Score], which name the type each question should have produced.
//
// It returns an [*APIError] for a non-2xx response that outlived the retry
// policy, a [*ConnectionError] for a request that never completed, and ctx's
// error when the caller gives up.
func (c *Client) SystemOne(ctx context.Context, request *SystemOneRequest, options *RequestOptions) (*SystemOneResult, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	// Copied rather than assigned into the caller's request, which is theirs
	// and may be reused for another call against another client.
	payload := *request
	if payload.Model == "" {
		payload.Model = c.defaultModel
	}

	var result SystemOneResult
	meta, err := c.do(ctx, http.MethodPost, "/v1/systemone", &payload, options, &result)
	if err != nil {
		return nil, err
	}
	// A 2xx with no answers is not a result, and handing one back would turn a
	// broken response into an empty one the caller has to diagnose later.
	if result.Answers == nil {
		return nil, errorf("POST /v1/systemone: %s carried no answers: %s", meta.describe(), meta.Body)
	}
	result.Meta = meta
	return &result, nil
}

// A call is one API call: the settings resolved from the client and the
// per-call options, and the behavior that turns them into a result.
//
// It holds its client rather than being passed to one, because every step of a
// call needs both — the settings for what to send and the client for the
// transport, the logger and the clock. Splitting them would mean threading two
// receivers' worth of state through the attempt loop, and the loop belongs to
// the call, not to the client that may be running a hundred of them.
type call struct {
	client *Client
	// tag numbers the call in the log. Concurrent calls interleave there, and
	// the attempts within one have to be tellable apart.
	tag        string
	method     string
	url        string
	body       []byte
	baseHeader http.Header
	timeout    time.Duration
	retry      RetryPolicy
}

// newCall resolves the settings of one call and encodes its body, so that a
// refused setting or an unencodable request costs no network attempt.
func (c *Client) newCall(method, path string, body any, options *RequestOptions) (*call, error) {
	var opts RequestOptions
	if options != nil {
		opts = *options
	}

	timeout := c.timeout
	switch {
	case opts.Timeout < 0:
		return nil, errorf("RequestOptions.Timeout must not be negative, got %s", opts.Timeout)
	case opts.Timeout > 0:
		timeout = opts.Timeout
	}

	retry := c.retry
	if opts.Retry != nil {
		if err := opts.Retry.validate("RequestOptions.Retry"); err != nil {
			return nil, err
		}
		retry = *opts.Retry
	}

	var encoded []byte
	if body != nil {
		var err error
		if encoded, err = json.Marshal(body); err != nil {
			return nil, errorf("encoding the request body: %w", err)
		}
	}

	return &call{
		client:     c,
		tag:        fmt.Sprintf("#%d %s %s", c.requests.Add(1), method, path),
		method:     method,
		url:        c.baseURL + path,
		body:       encoded,
		baseHeader: c.requestHeader(opts.Header, encoded),
		timeout:    timeout,
		retry:      retry,
	}, nil
}

// do runs one API call to completion and decodes a successful response into out.
func (c *Client) do(ctx context.Context, method, path string, body any, options *RequestOptions, out any) (Meta, error) {
	call, err := c.newCall(method, path, body, options)
	if err != nil {
		return Meta{}, err
	}
	return call.run(ctx, out)
}

// run sends the call and decodes a successful response into out, which may be
// nil for a caller that wants only the metadata. A response with no body to
// read leaves out untouched.
func (c *call) run(ctx context.Context, out any) (Meta, error) {
	meta, err := c.send(ctx)
	if err != nil {
		return Meta{}, err
	}
	if out == nil || len(meta.Body) == 0 {
		return meta, nil
	}
	if err = json.Unmarshal(meta.Body, out); err != nil {
		return meta, errorf("%s: decoding %s: %w", c.tag, meta.describe(), err)
	}
	return meta, nil
}

// send makes attempts until one succeeds, one fails in a way the policy does
// not retry, or the retries run out.
func (c *call) send(ctx context.Context) (Meta, error) {
	logger := c.client.logger
	for attempt := 0; ; attempt++ {
		retriesLeft := c.retry.MaxRetries - attempt
		header := c.headerFor(attempt)
		logger.DebugContext(ctx, "sending request",
			"request", c.tag, "attempt", attempt, "url", c.url,
			"header", redactedHeader(header), "body", jsonBody(c.body))

		started := time.Now()
		meta, err := c.attempt(ctx, header)
		if err != nil {
			if ctx.Err() != nil {
				return Meta{}, errorf("%s: %w", c.tag, context.Cause(ctx))
			}
			logger.InfoContext(ctx, "attempt failed",
				"request", c.tag, "attempt", attempt, "elapsed", time.Since(started), "error", err)
			if retriesLeft <= 0 || !c.retry.retriesError(err) {
				return Meta{}, err
			}
			if err = c.backOff(ctx, attempt, retriesLeft, err.Error(), nil); err != nil {
				return Meta{}, err
			}
			continue
		}

		logger.InfoContext(ctx, "received response",
			"request", c.tag, "attempt", attempt, "status", meta.Status,
			"elapsed", time.Since(started), "requestID", meta.RequestID)
		if meta.Status >= 200 && meta.Status < 300 {
			return meta, nil
		}

		logger.DebugContext(ctx, "error response",
			"request", c.tag, "attempt", attempt, "status", meta.Status, "body", jsonBody(meta.Body))
		if retriesLeft <= 0 || !c.retry.RetriesStatus(meta.Status) {
			return Meta{}, meta.apiError()
		}
		if err = c.backOff(ctx, attempt, retriesLeft, strconv.Itoa(meta.Status), meta.Header); err != nil {
			return Meta{}, err
		}
	}
}

// headerFor returns the headers of one zero-based attempt.
//
// The copy is per attempt because the header travels into the transport, and
// one attempt's retry count must not survive into the next.
func (c *call) headerFor(attempt int) http.Header {
	header := c.baseHeader.Clone()
	if attempt > 0 {
		header.Set(retryCountHeader, strconv.Itoa(attempt))
	}
	return header
}

// errAttemptTimeout distinguishes this package's per-attempt deadline from a
// deadline the caller's own context carried, which must not be retried.
var errAttemptTimeout = errors.New("attempt timeout")

// attempt makes one HTTP round trip and reads the whole response body under the
// attempt's timeout, so that a server that stalls mid-body is a failure this
// package can retry rather than one the caller discovers later.
func (c *call) attempt(ctx context.Context, header http.Header) (Meta, error) {
	attemptCtx, cancel := context.WithTimeoutCause(ctx, c.timeout, errAttemptTimeout)
	defer cancel()

	var body io.Reader
	if c.body != nil {
		body = bytes.NewReader(c.body)
	}
	request, err := http.NewRequestWithContext(attemptCtx, c.method, c.url, body)
	if err != nil {
		return Meta{}, errorf("building the request: %w", err)
	}
	request.Header = header

	response, err := c.client.httpClient.Do(request)
	if err != nil {
		return Meta{}, c.failed(ctx, attemptCtx, err)
	}
	raw, err := io.ReadAll(response.Body)
	// The body has been read to the end or has failed; either way its Close
	// tells the caller nothing they can act on, and the read error is the one
	// worth reporting.
	_ = response.Body.Close()
	if err != nil {
		return Meta{}, c.failed(ctx, attemptCtx, err)
	}
	return Meta{
		RequestID: response.Header.Get(requestIDHeader),
		Status:    response.StatusCode,
		Header:    response.Header,
		Body:      raw,
	}, nil
}

// failed classifies an attempt that produced no complete response.
//
// Only this package's own deadline becomes a timeout. A caller who gave up gets
// their error back unwrapped, for [call.send] to report as the context's cause:
// their cancellation is not a transport failure and is not retried.
func (c *call) failed(ctx, attemptCtx context.Context, err error) error {
	if errors.Is(context.Cause(attemptCtx), errAttemptTimeout) {
		return &ConnectionError{Timeout: c.timeout, Err: fmt.Errorf("%w: %w", context.DeadlineExceeded, err)}
	}
	if ctx.Err() != nil {
		return err
	}
	return &ConnectionError{Err: err}
}

// backOff waits before the next attempt, and reports the caller's error if they
// give up while it waits.
func (c *call) backOff(ctx context.Context, attempt, retriesLeft int, reason string, header http.Header) error {
	delay := c.retry.Delay(attempt, header, rand.Float64)
	c.client.logger.InfoContext(ctx, "retrying",
		"request", c.tag, "retry", attempt+1, "of", attempt+retriesLeft, "in", delay, "after", reason)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return errorf("%s: waiting to retry: %w", c.tag, context.Cause(ctx))
	}
}

// requestHeader assembles the headers for a request.
//
// The caller's headers are laid down first so that the headers below can
// overwrite them: a default header that displaced the Authorization or the
// content type would break the request in a way that is tedious to diagnose,
// and there is no use for that freedom worth the risk.
func (c *Client) requestHeader(callHeader http.Header, body []byte) http.Header {
	header := make(http.Header, len(c.header)+len(callHeader)+6)
	mergeHeader(header, c.header)
	mergeHeader(header, callHeader)

	header.Set("Authorization", "Bearer "+c.apiKey)
	header.Set("Accept", "application/json")
	header.Set("User-Agent", userAgent)
	header.Set(sdkHeader, userAgent)
	header.Set(runtimeHeader, runtimeDescription)
	header.Del(retryCountHeader)
	if body == nil {
		header.Del("Content-Type")
	} else {
		header.Set("Content-Type", "application/json")
	}
	return header
}

// mergeHeader copies src over dst, one whole field at a time, canonicalizing
// each name.
//
// Canonicalizing is what makes the overwrite in [Client.requestHeader] total. A
// caller's header map built as a literal keeps whatever capitalization it was
// written with, and a stray "authorization" that did not collide with the
// canonical "Authorization" would be sent alongside it rather than replaced by
// it, leaving the request with two.
func mergeHeader(dst, src http.Header) {
	for name, values := range src {
		dst[textproto.CanonicalMIMEHeaderKey(name)] = slices.Clone(values)
	}
}

// parseBody decodes a response body for reporting in an [APIError].
//
// It tries JSON whatever the content type says, because a proxy that replaces
// an error response often forgets to set one, and it falls back to the text so
// that an HTML error page still reaches the caller instead of being discarded.
func parseBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}

// A jsonBody logs a request or response body as JSON when a handler records the
// message, and as text when it is not valid JSON.
type jsonBody []byte

func (b jsonBody) LogValue() slog.Value {
	if len(b) == 0 {
		return slog.AnyValue(nil)
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return slog.StringValue(string(b))
	}
	return slog.AnyValue(value)
}
