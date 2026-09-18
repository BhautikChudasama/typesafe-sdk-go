package typesafe_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	typesafe "github.com/Tangerg/typesafe-sdk-go"
)

// The rest of the suite swaps in a RoundTripper, which is the right way to test
// what this package decides. It is the wrong way to test what actually reaches a
// socket: a custom transport skips everything net/http does on the way out. The
// tests here go through a real server so that the bytes on the wire are the
// thing being checked.

// liveServer starts an HTTP server and returns a client pointed at it.
func liveServer(t *testing.T, handler http.HandlerFunc, options *typesafe.ClientOptions) *typesafe.Client {
	t.Helper()
	clearEnv(t)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	var opts typesafe.ClientOptions
	if options != nil {
		opts = *options
	}
	opts.APIKey = "wire-key"
	opts.BaseURL = server.URL
	if opts.Retry == nil {
		policy := typesafe.DefaultRetryPolicy()
		policy.MaxRetries = 0
		opts.Retry = &policy
	}

	client, err := typesafe.NewClient(&opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// TestWireHeaders checks the request as a server sees it: each header this
// package owns arrives exactly once, with the value this package chose, however
// the caller spelled theirs.
func TestWireHeaders(t *testing.T) {
	var got http.Header
	var gotBody []byte
	client := liveServer(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Typesafe-Request-Id", "req_wire")
		_ = json.NewEncoder(w).Encode(systemOneResponse)
	}, &typesafe.ClientOptions{
		Header: http.Header{"authorization": {"bad"}, "X-Team": {"default"}},
	})

	result, err := client.SystemOne(t.Context(),
		&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("?")},
		&typesafe.RequestOptions{Header: http.Header{"CONTENT-TYPE": {"text/html"}, "x-team": {"call"}}})
	if err != nil {
		t.Fatalf("SystemOne: %v", err)
	}
	if result.Meta.RequestID != "req_wire" {
		t.Errorf("RequestID = %q", result.Meta.RequestID)
	}

	for name, want := range map[string]string{
		"Authorization": "Bearer wire-key",
		"Accept":        "application/json",
		"Content-Type":  "application/json",
		"X-Team":        "call",
	} {
		if values := got.Values(name); len(values) != 1 || values[0] != want {
			t.Errorf("the server saw %s = %q, want exactly [%q]", name, values, want)
		}
	}
	if ua := got.Values("User-Agent"); len(ua) != 1 || !strings.HasPrefix(ua[0], "typesafe-sdk-go/") {
		t.Errorf("the server saw User-Agent = %q, want one set by this package", ua)
	}
	// net/http sets Content-Length from the buffered body; a request that left
	// it unset would be sent chunked, which some gateways refuse.
	if got := len(gotBody); got == 0 {
		t.Error("the server received an empty body")
	}
}

// TestWireBodyIsResentOnRetry checks the part of a retry that is easy to get
// wrong: the body is a reader, and a second attempt needs its own.
func TestWireBodyIsResentOnRetry(t *testing.T) {
	fast := typesafe.DefaultRetryPolicy()
	fast.MaxRetries = 1
	fast.BackoffInitial = 0
	fast.BackoffMax = 0

	var mu sync.Mutex
	var bodies []string
	client := liveServer(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		attempt := len(bodies)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"detail":"busy"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(systemOneResponse)
	}, &typesafe.ClientOptions{Retry: &fast})

	if _, err := client.SystemOne(t.Context(),
		&typesafe.SystemOneRequest{State: "s", Questions: noulQuestion("?")}, nil); err != nil {
		t.Fatalf("SystemOne: %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("the server saw %d requests, want 2", len(bodies))
	}
	if bodies[0] == "" || bodies[0] != bodies[1] {
		t.Errorf("the retry sent a different body:\n%q\n%q", bodies[0], bodies[1])
	}
}

// TestWireGETSendsNoBody checks that a GET goes out without a body or a content
// type, which a request built from a non-nil empty reader would not.
func TestWireGETSendsNoBody(t *testing.T) {
	var contentType string
	var length int64
	client := liveServer(t, func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		length = r.ContentLength
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}, nil)

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if contentType != "" {
		t.Errorf("the server saw Content-Type = %q on a GET, want none", contentType)
	}
	if length > 0 {
		t.Errorf("the server saw a %d-byte body on a GET, want none", length)
	}
}

// TestWireLoggingLeavesTheRequestIntact is the failure that redaction done in
// place would cause: the log would look right and the request would go out with
// the masked key.
func TestWireLoggingLeavesTheRequestIntact(t *testing.T) {
	var buf strings.Builder
	var seen string
	client := liveServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}, &typesafe.ClientOptions{Logger: debugLogger(&buf)})

	if _, err := client.ListModels(t.Context(), nil); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if seen != "Bearer wire-key" {
		t.Errorf("the server saw Authorization = %q, want the real key", seen)
	}
	if logged := buf.String(); strings.Contains(logged, "wire-key") {
		t.Errorf("the log leaked the key:\n%s", logged)
	}
	if logged := buf.String(); !strings.Contains(logged, "***") {
		t.Errorf("the log did not record a redacted Authorization:\n%s", logged)
	}
}

// TestWireConcurrentCallsAreNumbered guards the counter that makes a log of
// interleaved calls readable at all.
func TestWireConcurrentCallsAreNumbered(t *testing.T) {
	var buf lockedBuilder
	client := liveServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	}, &typesafe.ClientOptions{Logger: debugLogger(&buf)})

	const calls = 8
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			if _, err := client.ListModels(t.Context(), nil); err != nil {
				t.Errorf("ListModels: %v", err)
			}
		})
	}
	wg.Wait()

	logged := buf.String()
	for i := 1; i <= calls; i++ {
		tag := "#" + strconv.Itoa(i) + " GET /v1/models"
		if !strings.Contains(logged, tag) {
			t.Errorf("no log line is tagged %q; concurrent calls cannot be told apart", tag)
		}
	}
}

// --- helpers ---

// lockedBuilder is a strings.Builder safe for the concurrent writes a handler
// makes from several goroutines.
type lockedBuilder struct {
	mu      sync.Mutex
	builder strings.Builder
}

func (b *lockedBuilder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.Write(p)
}

func (b *lockedBuilder) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.builder.String()
}
