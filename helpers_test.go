package typesafe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	typesafe "github.com/BhautikChudasama/typesafe-sdk-go"
)

// A recordedRequest is one request the transport saw, with its body already
// read so that a test can assert on it after the call returns.
type recordedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
	// Ctx is the attempt's context, which carries the per-attempt timeout. A
	// responder that means to stand in for a slow server must wait on it, as a
	// real transport does.
	Ctx context.Context //nolint:containedctx // a recorded request is exactly the transport's view of one
}

// JSON decodes the request body, failing the test if it is not JSON.
func (r recordedRequest) JSON(t *testing.T) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(r.Body, &value); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, r.Body)
	}
	return value
}

// A responder answers the nth request the client makes, counting from zero.
type responder func(attempt int, req recordedRequest) (*http.Response, error)

// A recorder is an [http.RoundTripper] that records requests and answers them
// from a responder, standing in for the service.
type recorder struct {
	respond responder

	mu       sync.Mutex
	requests []recordedRequest
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		if body, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		_ = req.Body.Close()
	}
	recorded := recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Header: req.Header.Clone(),
		Body:   body,
		Ctx:    req.Context(),
	}

	r.mu.Lock()
	attempt := len(r.requests)
	r.requests = append(r.requests, recorded)
	r.mu.Unlock()

	resp, err := r.respond(attempt, recorded)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

// Requests reports the requests seen so far.
func (r *recorder) Requests() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedRequest(nil), r.requests...)
}

// Last reports the most recent request, failing the test when there is none.
func (r *recorder) Last(t *testing.T) recordedRequest {
	t.Helper()
	requests := r.Requests()
	if len(requests) == 0 {
		t.Fatal("no request was sent")
	}
	return requests[len(requests)-1]
}

// jsonResponse builds a JSON response with the given status.
func jsonResponse(status int, body any, header http.Header) *http.Response {
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return rawResponse(status, encoded, "application/json", header)
}

// rawResponse builds a response from bytes, for bodies that are not JSON.
func rawResponse(status int, body []byte, contentType string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	} else {
		header = header.Clone()
	}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// always answers every request the same way.
func always(status int, body any) responder {
	return func(int, recordedRequest) (*http.Response, error) {
		return jsonResponse(status, body, nil), nil
	}
}

// hang stands in for a server that never answers, leaving the attempt to run
// out of time the way a real transport would.
func hang() responder {
	return func(_ int, req recordedRequest) (*http.Response, error) {
		<-req.Ctx.Done()
		return nil, req.Ctx.Err()
	}
}

// A stallingBody stands in for a response whose headers arrived but whose body
// never finishes. The real transport aborts such a read when the request's
// context expires; this reproduces that so a test can check what the SDK makes
// of it.
type stallingBody struct {
	ctx context.Context //nolint:containedctx // reproduces the transport tying a body read to the request
}

func (b stallingBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b stallingBody) Close() error { return nil }

// A failingBody stands in for a connection dropped part-way through the body.
type failingBody struct{ err error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.err }
func (b failingBody) Close() error             { return nil }

// withBody replaces a response's body, for the two cases above.
func withBody(status int, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          body,
		ContentLength: -1,
	}
}

// debugLogger records everything the SDK logs, for the tests that check what
// reaches a handler and what must not.
func debugLogger(sink io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(sink, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// clearEnv unsets every variable the SDK reads, so that a developer's own
// environment cannot change what a test observes.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		typesafe.EnvAPIKey, typesafe.EnvBaseURL, typesafe.EnvDefaultModel, typesafe.EnvLogLevel,
	} {
		t.Setenv(name, "")
	}
}

// newTestClient returns a client whose transport is the returned recorder.
// Retries are disabled unless options say otherwise, because a test that means
// to see one failure should not silently see three.
func newTestClient(t *testing.T, respond responder, options *typesafe.ClientOptions) (*typesafe.Client, *recorder) {
	t.Helper()
	clearEnv(t)

	var opts typesafe.ClientOptions
	if options != nil {
		opts = *options
	}
	if opts.APIKey == "" {
		opts.APIKey = "test-key"
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.test"
	}
	if opts.Retry == nil {
		policy := typesafe.DefaultRetryPolicy()
		policy.MaxRetries = 0
		opts.Retry = &policy
	}

	rec := &recorder{respond: respond}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Transport: rec}
	}

	client, err := typesafe.NewClient(&opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, rec
}

// A noulQuestion is the smallest valid question, for tests about something
// other than the questions themselves.
func noulQuestion(instructions typesafe.Entry) typesafe.Questions {
	return typesafe.Questions{"q": &typesafe.NoulQuestion{Instructions: instructions}}
}

// systemOneResponse is a minimal well-formed answer to noulQuestion.
var systemOneResponse = map[string]any{
	"model": "jev-1.13",
	"answers": map[string]any{
		"q": map[string]any{"type": "noul", "noul": 0.5},
	},
	"usage": map[string]any{"input_tokens": 1, "output_tokens": 2},
}
